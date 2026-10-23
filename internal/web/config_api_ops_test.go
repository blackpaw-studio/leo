package web

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/history"
)

func TestTaskRunStartsLeoRun(t *testing.T) {
	h := newAPIHarness(t)
	var got []string
	h.svc.leoPath = "/bin/leo"
	h.svc.execCommand = func(name string, args ...string) *exec.Cmd {
		got = append([]string{name}, args...)
		return exec.Command("true")
	}
	res := h.do(http.MethodPost, "/tasks/heartbeat/run", "", nil)
	wantStatus(t, res, http.StatusAccepted)
	if d := res.data(); d["name"] != "heartbeat" || d["status"] != "started" {
		t.Errorf("data = %v", d)
	}
	want := []string{"/bin/leo", "run", "heartbeat", "--config", h.path}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v, want %v", got, want)
	}
	wantStatus(t, h.do(http.MethodPost, "/tasks/ghost/run", "", nil), http.StatusNotFound)
}

func TestTaskRunReportsSpawnFailure(t *testing.T) {
	h := newAPIHarness(t)
	h.svc.execCommand = func(string, ...string) *exec.Cmd { return exec.Command("/nonexistent/leo") }
	wantStatus(t, h.do(http.MethodPost, "/tasks/heartbeat/run", "", nil), http.StatusInternalServerError)
}

func TestTaskHistoryNewestFirstWithLimit(t *testing.T) {
	h := newAPIHarness(t)
	cfg := h.loadConfig()
	store := history.NewStore(cfg.HomePath)
	for i := 0; i < 3; i++ {
		if err := store.RecordTimed("heartbeat", i, "", "", time.Unix(int64(1000+i), 0), 5); err != nil {
			t.Fatal(err)
		}
	}
	res := h.do(http.MethodGet, "/tasks/heartbeat/history", "", nil)
	wantStatus(t, res, http.StatusOK)
	list := res.Body["data"].([]any)
	if len(list) != 3 || list[0].(map[string]any)["exit_code"] != float64(2) {
		t.Fatalf("history = %v, want newest (exit 2) first", list)
	}
	res = h.do(http.MethodGet, "/tasks/heartbeat/history?limit=1", "", nil)
	if got := res.Body["data"].([]any); len(got) != 1 {
		t.Errorf("limit=1 returned %d entries", len(got))
	}
	wantStatus(t, h.do(http.MethodGet, "/tasks/heartbeat/history?limit=abc", "", nil), http.StatusBadRequest)
	res = h.do(http.MethodGet, "/tasks/nightly/history", "", nil)
	wantStatus(t, res, http.StatusOK)
	if got, ok := res.Body["data"].([]any); !ok || len(got) != 0 {
		t.Errorf("empty history must be [], got %s", res.Raw)
	}
	wantStatus(t, h.do(http.MethodGet, "/tasks/ghost/history", "", nil), http.StatusNotFound)
}

func TestTaskRunLogGuards(t *testing.T) {
	h := newAPIHarness(t)
	cfg := h.loadConfig()
	logs := filepath.Join(cfg.StatePath(), "logs")
	if err := os.MkdirAll(logs, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "heartbeat-1.jsonl"), []byte(`{"type":"result"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "other-1.jsonl"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := h.do(http.MethodGet, "/tasks/heartbeat/log?file=heartbeat-1.jsonl", "", nil)
	wantStatus(t, res, http.StatusOK)
	if d := res.data(); d["file"] != "heartbeat-1.jsonl" || !strings.Contains(d["text"].(string), `"result"`) {
		t.Errorf("data = %v", d)
	}
	wantStatus(t, h.do(http.MethodGet, "/tasks/heartbeat/log", "", nil), http.StatusBadRequest)
	wantStatus(t, h.do(http.MethodGet, "/tasks/heartbeat/log?file=other-1.jsonl", "", nil), http.StatusBadRequest)
	wantStatus(t, h.do(http.MethodGet, "/tasks/heartbeat/log?file=heartbeat-9.jsonl", "", nil), http.StatusNotFound)
	// Only the basename is honored, so traversal cannot leave the logs dir.
	res = h.do(http.MethodGet, "/tasks/heartbeat/log?file=../../heartbeat-1.jsonl", "", nil)
	if res.Status == http.StatusOK && res.data()["file"] != "heartbeat-1.jsonl" {
		t.Errorf("traversal leaked: %s", res.Raw)
	}
	wantStatus(t, h.do(http.MethodGet, "/tasks/ghost/log?file=ghost-1", "", nil), http.StatusNotFound)
}

func TestTaskPromptReadWrite(t *testing.T) {
	h := newAPIHarness(t)
	cfg := h.loadConfig()
	ws := cfg.TaskWorkspace(cfg.Tasks["heartbeat"])
	if err := os.MkdirAll(ws, 0o750); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(ws, "heartbeat.md")
	if err := os.WriteFile(prompt, []byte("check the thing\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := h.do(http.MethodGet, "/config/tasks/heartbeat/prompt", "", nil)
	wantStatus(t, res, http.StatusOK)
	d := res.data()
	rev, _ := d["revision"].(string)
	if d["file"] != "heartbeat.md" || d["content"] != "check the thing\n" || rev != config.RevisionOf([]byte("check the thing\n")) {
		t.Fatalf("doc = %v", d)
	}
	if files, _ := d["files"].([]any); len(files) == 0 {
		t.Errorf("files = %v", d["files"])
	}

	wantStatus(t, h.do(http.MethodPut, "/config/tasks/heartbeat/prompt", "", map[string]any{"content": "x"}), http.StatusPreconditionRequired)
	res = h.do(http.MethodPut, "/config/tasks/heartbeat/prompt", "sha256:stale", map[string]any{"content": "x"})
	wantStatus(t, res, http.StatusConflict)
	if res.data()["revision"] != rev {
		t.Errorf("409 data = %v, want current prompt revision", res.data())
	}
	if b, _ := os.ReadFile(prompt); string(b) != "check the thing\n" {
		t.Error("a refused write changed the prompt")
	}

	res = h.do(http.MethodPut, "/config/tasks/heartbeat/prompt", rev, map[string]any{"content": "new body"})
	wantStatus(t, res, http.StatusOK)
	if res.data()["revision"] != config.RevisionOf([]byte("new body")) {
		t.Errorf("revision = %v", res.data()["revision"])
	}
	if b, _ := os.ReadFile(prompt); string(b) != "new body" {
		t.Errorf("prompt = %q", b)
	}
	wantStatus(t, h.do(http.MethodGet, "/config/tasks/ghost/prompt", "", nil), http.StatusNotFound)
}

func TestTaskPromptCreatesMissingFile(t *testing.T) {
	h := newAPIHarness(t)
	cfg := h.loadConfig()
	ws := cfg.TaskWorkspace(cfg.Tasks["nightly"])
	res := h.do(http.MethodGet, "/config/tasks/nightly/prompt", "", nil)
	wantStatus(t, res, http.StatusOK)
	d := res.data()
	if d["content"] != "" || d["revision"] != config.RevisionOf(nil) {
		t.Fatalf("missing file doc = %v", d)
	}
	wantStatus(t, h.do(http.MethodPut, "/config/tasks/nightly/prompt", d["revision"].(string), map[string]any{"content": "hello"}), http.StatusOK)
	if b, err := os.ReadFile(filepath.Join(ws, "prompts", "nightly.md")); err != nil || string(b) != "hello" {
		t.Errorf("created file = %q, %v", b, err)
	}
}

func TestTaskPromptFileParamCannotEscapeWorkspace(t *testing.T) {
	h := newAPIHarness(t)
	res := h.do(http.MethodGet, "/config/tasks/heartbeat/prompt?file=../../etc/passwd", "", nil)
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if f := res.errorFields(); len(f) == 0 || f[0]["field"] != "file" {
		t.Errorf("errors = %v", f)
	}
}

func TestCronPreviewAPI(t *testing.T) {
	h := newAPIHarness(t)
	res := h.do(http.MethodGet, "/cron/preview?expr=0+3+*+*+*", "", nil)
	wantStatus(t, res, http.StatusOK)
	if d := res.data(); d["valid"] != true || d["description"] == "" {
		t.Errorf("valid = %v", d)
	}
	res = h.do(http.MethodGet, "/cron/preview?expr=nope", "", nil)
	wantStatus(t, res, http.StatusOK)
	if d := res.data(); d["valid"] != false || d["error"] == "" {
		t.Errorf("invalid = %v", d)
	}
	wantStatus(t, h.do(http.MethodGet, "/cron/preview", "", nil), http.StatusBadRequest)
}

func TestServiceRestartSpawnsGracefulRestartAndClearsFlag(t *testing.T) {
	h := newAPIHarness(t)
	var got []string
	h.svc.leoPath = "/bin/leo"
	h.svc.execCommand = func(name string, args ...string) *exec.Cmd {
		got = append([]string{name}, args...)
		return exec.Command("true")
	}
	h.svc.serviceRestartNeeded.Store(true)
	wantStatus(t, h.do(http.MethodPost, "/service/restart", "", nil), http.StatusAccepted)
	want := "/bin/leo service restart --graceful --force --config " + h.path
	if strings.Join(got, " ") != want {
		t.Errorf("argv = %q, want %q", strings.Join(got, " "), want)
	}
	if h.svc.RestartFlags().Service {
		t.Error("service flag not cleared")
	}
}

func TestServiceRestartAgentsClearsFlagOnlyOnCleanBatch(t *testing.T) {
	h := newAPIHarness(t)
	mock := &mockAgentService{restartAllResult: agent.RestartResult{
		Restarted: []string{"a"}, Skipped: []string{"d"}, Failed: map[string]error{"b": errors.New("boom")},
	}}
	h.svc.agents = func() AgentRestarter { return mock }
	h.svc.agentsRestartNeeded.Store(true)

	res := h.do(http.MethodPost, "/service/restart-agents", "", nil)
	wantStatus(t, res, http.StatusOK)
	d := res.data()
	if f, _ := d["failed"].(map[string]any); f["b"] != "boom" {
		t.Errorf("failed = %v", d["failed"])
	}
	if r, _ := d["restarted"].([]any); len(r) != 1 {
		t.Errorf("restarted = %v", d["restarted"])
	}
	if !h.svc.RestartFlags().Agents {
		t.Error("flag cleared despite a failure")
	}

	mock.restartAllResult = agent.RestartResult{Restarted: []string{"a"}}
	res = h.do(http.MethodPost, "/service/restart-agents", "", nil)
	if f, ok := res.data()["failed"].(map[string]any); !ok || len(f) != 0 {
		t.Errorf("clean batch failed = %s", res.Raw)
	}
	if h.svc.RestartFlags().Agents {
		t.Error("flag not cleared after a clean batch")
	}

	h.svc.agents = nil
	wantStatus(t, h.do(http.MethodPost, "/service/restart-agents", "", nil), http.StatusServiceUnavailable)
}

func TestServiceLogAPITail(t *testing.T) {
	h := newAPIHarness(t)
	wantStatus(t, h.do(http.MethodGet, "/service/log", "", nil), http.StatusNotFound)

	logPath := filepath.Join(t.TempDir(), "leo.log")
	h.svc.serviceLogPath = logPath
	res := h.do(http.MethodGet, "/service/log", "", nil)
	wantStatus(t, res, http.StatusOK)
	if res.data()["text"] != "" {
		t.Errorf("no log yet must be empty text, got %v", res.data())
	}
	if err := os.WriteFile(logPath, []byte("one\ntwo\nthree\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res = h.do(http.MethodGet, "/service/log?lines=2", "", nil)
	if res.data()["text"] != "two\nthree" {
		t.Errorf("tail = %q", res.data()["text"])
	}
	wantStatus(t, h.do(http.MethodGet, "/service/log?lines=0", "", nil), http.StatusBadRequest)
}

type fakeProcesses map[string]ProcessStateInfo

func (f fakeProcesses) States() map[string]ProcessStateInfo { return f }

func TestServiceStatusAPI(t *testing.T) {
	h := newAPIHarness(t)
	started := time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC)
	h.svc.version = func() string { return "v9.9.9" }
	h.svc.pid, h.svc.startedAt = 4242, started
	h.svc.serviceLogPath = "/var/log/leo.log"
	h.svc.processes = func() ProcessStateProvider {
		return fakeProcesses{"b": {Name: "b", Status: "running", Restarts: 2}, "a": {Name: "a", Status: "stopped", Ephemeral: true}}
	}
	h.svc.agentsRestartNeeded.Store(true)

	res := h.do(http.MethodGet, "/service/status", "", nil)
	wantStatus(t, res, http.StatusOK)
	d := res.data()
	if d["version"] != "v9.9.9" || d["pid"] != float64(4242) || d["config_path"] != h.path || d["log_path"] != "/var/log/leo.log" {
		t.Errorf("status = %v", d)
	}
	if d["started_at"] != "2026-10-23T08:00:00Z" {
		t.Errorf("started_at = %v", d["started_at"])
	}
	if rr := d["restart_required"].(map[string]any); rr["agents"] != true || rr["service"] != false {
		t.Errorf("restart_required = %v", rr)
	}
	procs := d["processes"].([]any)
	if len(procs) != 2 || procs[0].(map[string]any)["name"] != "a" || procs[1].(map[string]any)["restarts"] != float64(2) {
		t.Errorf("processes = %v, want sorted by name", procs)
	}
}

func TestConfigReloadAPI(t *testing.T) {
	rel := &mockReloader{}
	svc, path := newConfigService(t, rel)
	h := newAPIHarnessOver(t, svc, path, "")
	svc.RegisterReloadRoute(h.mux, "", func(f http.HandlerFunc) http.HandlerFunc { return f })
	res := h.do(http.MethodPost, "/config/reload", "", nil)
	wantStatus(t, res, http.StatusOK)
	if !rel.called || res.data()["revision"] != h.revision() {
		t.Errorf("called=%v data=%v", rel.called, res.data())
	}

	rel.err = errors.New("scheduler down")
	res = h.do(http.MethodPost, "/config/reload", "", nil)
	wantStatus(t, res, http.StatusInternalServerError)
}

// The config API is operator-only on the web: an agent's token reaches the
// rest of /api/* but must not read or edit leo.yaml.
func TestWebConfigAPIIsOperatorOnly(t *testing.T) {
	s := newTokenSplitServer(t)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/config/schema", ""},
		{http.MethodGet, "/api/v1/config/delegation", ""},
		{http.MethodPut, "/api/v1/config/defaults", `{"values":{}}`},
		{http.MethodPost, "/api/v1/tasks/x/run", ""},
		{http.MethodPost, "/api/v1/service/restart", ""},
		{http.MethodPost, "/api/v1/config/reload", ""},
		{http.MethodGet, "/api/v1/version", ""},
	} {
		if w := requestAs(t, s, tc.method, tc.path, testAgentToken, tc.body); w.Code != http.StatusForbidden {
			t.Errorf("%s %s with the agent token = %d, want 403", tc.method, tc.path, w.Code)
		}
		if w := requestAs(t, s, tc.method, tc.path, "", tc.body); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token = %d, want 401", tc.method, tc.path, w.Code)
		}
	}
	if w := requestAs(t, s, http.MethodGet, "/api/v1/version", testAPIToken, ""); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"config_api"`) {
		t.Errorf("GET /api/v1/version as operator = %d %s", w.Code, w.Body.String())
	}
	w := requestAs(t, s, http.MethodGet, "/api/v1/config/schema", testAPIToken, "")
	if w.Code != http.StatusOK {
		t.Errorf("GET /api/v1/config/schema as operator = %d %s", w.Code, w.Body.String())
	}
}

// The apps link "Edit in the web UI" to these pages; there is no /config index.
func TestWebUIPagesTheAppsLinkTo(t *testing.T) {
	s := newTokenSplitServer(t)
	for _, path := range []string{
		"/tasks", "/tasks/heartbeat", "/config/defaults", "/config/templates", "/config/templates/coding",
		"/config/environments", "/config/delegation", "/config/settings", "/service",
	} {
		if w := requestAs(t, s, http.MethodGet, path, testAPIToken, ""); w.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, w.Code)
		}
	}
	if w := requestAs(t, s, http.MethodGet, "/config", testAPIToken, ""); w.Code != http.StatusNotFound {
		t.Errorf("GET /config = %d, want 404 (there is no config index page)", w.Code)
	}
}

// A run log is read from its tail, never whole: a log over the cap returns
// only the last bytes (cut to whole lines) and says so.
func TestTaskLogReadsABoundedTail(t *testing.T) {
	h := newAPIHarness(t)
	cfg := h.loadConfig()
	logs := filepath.Join(cfg.StatePath(), "logs")
	if err := os.MkdirAll(logs, 0o750); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "line-%03d\n", i)
	}
	if err := os.WriteFile(filepath.Join(logs, "heartbeat-big.jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	h.svc.maxLogBytes = 64

	res := h.do(http.MethodGet, "/tasks/heartbeat/log?file=heartbeat-big.jsonl", "", nil)
	wantStatus(t, res, http.StatusOK)
	d := res.data()
	text, _ := d["text"].(string)
	if d["truncated"] != true || len(text) > 64 || !strings.HasPrefix(text, "line-") || !strings.HasSuffix(b.String(), text) || !strings.HasSuffix(text, "line-199\n") {
		t.Errorf("tail = %q (truncated=%v)", text, d["truncated"])
	}

	h.svc.maxLogBytes = 1 << 20
	res = h.do(http.MethodGet, "/tasks/heartbeat/log?file=heartbeat-big.jsonl", "", nil)
	if d := res.data(); d["text"] != b.String() || d["truncated"] != nil {
		t.Errorf("a log under the cap must come back whole, truncated=%v len=%d", d["truncated"], len(d["text"].(string)))
	}
}

// Service restart is single-flight: while `leo service restart` is still
// running, another request is a 409, not a second restart.
func TestServiceRestartIsSingleFlight(t *testing.T) {
	h := newAPIHarness(t)
	var started int
	var proc *exec.Cmd
	h.svc.execCommand = func(string, ...string) *exec.Cmd {
		started++
		proc = exec.Command("sleep", "30")
		return proc
	}
	wantStatus(t, h.do(http.MethodPost, "/service/restart", "", nil), http.StatusAccepted)
	t.Cleanup(func() { _ = proc.Process.Kill() })

	res := h.do(http.MethodPost, "/service/restart", "", nil)
	wantStatus(t, res, http.StatusConflict)
	wantCode(t, res, CodeRestartInProgress)
	if started != 1 {
		t.Fatalf("a second restart was spawned (%d starts)", started)
	}

	// Once the restart process is gone (it failed, say), a retry is allowed.
	_ = proc.Process.Kill()
	deadline := time.Now().Add(5 * time.Second)
	for h.do(http.MethodPost, "/service/restart", "", nil).Status == http.StatusConflict {
		if time.Now().After(deadline) {
			t.Fatal("restart stayed in flight after its process exited")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if started != 2 {
		t.Errorf("starts = %d, want 2", started)
	}
	_ = proc.Process.Kill()
}

// Every config API route is operator-only on the web: the agent token (which
// reaches the rest of /api/*) and API-client tokens (which reach only message
// and chat-post) are refused on every one, and an anonymous caller is 401.
// The route list comes from the registration, so a new route is covered here
// without an edit.
func TestEveryWebConfigRouteRejectsAgentAndClientTokens(t *testing.T) {
	s := newTokenSplitServer(t)
	s.SetClients([]ClientPolicy{{Name: "pipe", Token: testPostToken, CanPost: []string{"pipe-*"}}})
	patterns := append(ConfigRoutePatterns("/api/v1"), "GET /api/v1/version")
	if len(patterns) < 30 {
		t.Fatalf("only %d config routes listed; the registration walk is broken", len(patterns))
	}
	wildcard := regexp.MustCompile(`\{[^}]+\}`)
	for _, pattern := range patterns {
		method, path, _ := strings.Cut(pattern, " ")
		path = wildcard.ReplaceAllString(path, "x")
		for name, token := range map[string]string{"agent": testAgentToken, "client": testPostToken} {
			if w := requestAs(t, s, method, path, token, ""); w.Code != http.StatusForbidden {
				t.Errorf("%s with the %s token = %d, want 403", pattern, name, w.Code)
			}
		}
		if w := requestAs(t, s, method, path, "", ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s without a token = %d, want 401", pattern, w.Code)
		}
	}
}
