//go:build e2e

// Package e2e: driver_test.go covers the codex/opencode session-driver
// flows added by Plan 4 (Tasks 3-7) against the fake binaries — the seams
// e2e already uses (daemon boot, agent.Manager, the web message endpoint,
// and the real harness-aware injector) rather than real tmux, which is
// never exercised in this package (see e2e_test.go's fakes and
// persistent_helpers_test.go's mock-injector precedent).
//
// codex (TurnDriver, DriveTurns — no resident process) is fully exercisable
// here: superviseTurnBased (internal/service) never touches tmux, so an
// agent.Manager wired to a small test-double Supervisor (see
// codexFakeSupervisor below) that runs the REAL codexharness.TurnDriver
// drives the same production code fakeclaude/fakecodex-style task tests
// already rely on. The second message in the codex agent flow goes through
// the real web message endpoint (handleProcessMessage ->
// dispatchNonClaudeMessage), which is genuinely tmux-free for non-claude
// targets.
//
// opencode (ServerDriver, DriveTmux — a resident `opencode serve`) is NOT
// spawnable through agent.Manager here: the supervisor's process/agent
// restart loop for a DriveTmux harness always goes through a real tmux
// session. So the opencode flow below drives the driver directly
// (EnsureServerState + ServerDriver.Inject), which is exactly the seam
// dispatchNonClaudeMessage itself calls in production — the only thing not
// exercised is the resident `opencode serve` pane itself, deferred to the
// gated real-binary smoke test in harness_task_test.go.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/harness"
	codexharness "github.com/blackpaw-studio/leo/internal/harness/codex"
	opencodeharness "github.com/blackpaw-studio/leo/internal/harness/opencode"
	"github.com/blackpaw-studio/leo/internal/history"
	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/blackpaw-studio/leo/internal/web"
)

// fakeBinDir returns the directory containing fakeclaude/fakecodex/
// fakeopencode (all built into the same TestMain temp dir), so in-process
// driver calls (not run through runLeo's subprocess PATH) can find the
// fakes too.
func fakeBinDir() string {
	return filepath.Dir(fakeclaude)
}

// pollUntil waits up to timeout for cond to return true, polling every
// interval. Returns false on timeout.
func pollUntil(timeout, interval time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(interval)
	}
}

// freeLocalPort allocates an ephemeral localhost TCP port via a
// throwaway listen-then-close, for tests that need to pre-pick a port
// (e.g. the web server's Options.Port, which must match the listener).
func freeLocalPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocating free port: %v", err)
	}
	defer l.Close()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected listener addr type %T", l.Addr())
	}
	return addr.Port
}

// --- codex agent flow (daemon/manager path, no tmux) ---

// codexHandleForRequest mirrors service.handleForSpec for the codex path:
// the SessionIDStore is agentstore-backed (agent.NewAgentIDs), matching
// what agent.Manager.ResolveHandle builds in production.
func codexHandleForRequest(req agent.SpawnRequest, homePath string) harness.SessionHandle {
	return harness.SessionHandle{
		Kind:          harness.KindAgent,
		Name:          req.Name,
		TmuxSession:   agent.SessionName(req.Name),
		Workspace:     req.WorkDir,
		HomePath:      homePath,
		Env:           req.Env,
		TurnArgs:      req.ClaudeArgs,
		OpeningPrompt: req.OpeningPrompt,
		IDs:           agent.NewAgentIDs(homePath, req.Name),
	}
}

// codexFakeSupervisor implements agent.Supervisor for a DriveTurns harness
// without going through service.Supervisor/RunSupervised, whose boot path
// unconditionally requires a real tmux binary on PATH (findTmux) even
// though codex's own TurnDriver never touches it. SpawnAgent runs the REAL
// codexharness.TurnDriver.Start in a background goroutine — the same call
// service.superviseTurnBased makes — so the opening turn genuinely reaches
// the fakecodex binary on PATH.
type codexFakeSupervisor struct {
	homePath string

	mu     sync.Mutex
	states map[string]agent.ProcessState
}

func newCodexFakeSupervisor(homePath string) *codexFakeSupervisor {
	return &codexFakeSupervisor{homePath: homePath, states: map[string]agent.ProcessState{}}
}

func (s *codexFakeSupervisor) ReserveAgent(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.states[name]; ok {
		return fmt.Errorf("agent %q already exists", name)
	}
	return nil
}

func (s *codexFakeSupervisor) ReleaseAgent(string) {}

func (s *codexFakeSupervisor) SpawnAgent(req agent.SpawnRequest) error {
	s.mu.Lock()
	if _, ok := s.states[req.Name]; ok {
		s.mu.Unlock()
		return fmt.Errorf("agent %q already exists", req.Name)
	}
	s.states[req.Name] = agent.ProcessState{Name: req.Name, Status: "running", StartedAt: time.Now(), Ephemeral: true}
	s.mu.Unlock()

	go func() {
		drv := codexharness.TurnDriver{}
		handle := codexHandleForRequest(req, s.homePath)
		if err := drv.Start(context.Background(), handle); err != nil {
			fmt.Fprintf(os.Stderr, "codexFakeSupervisor: driver start: %v\n", err)
		}
	}()
	return nil
}

func (s *codexFakeSupervisor) StopAgent(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.states[name]; !ok {
		return fmt.Errorf("agent %q not found", name)
	}
	delete(s.states, name)
	return nil
}

func (s *codexFakeSupervisor) RenameAgent(string, string) error { return nil }

func (s *codexFakeSupervisor) EphemeralAgents() map[string]agent.ProcessState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]agent.ProcessState, len(s.states))
	for k, v := range s.states {
		out[k] = v
	}
	return out
}

// readAgentRecord polls the agentstore for name up to timeout, returning
// the record once found (or the zero value + false on timeout).
func readAgentRecord(t *testing.T, homePath, name string, timeout time.Duration) (agentstore.Record, bool) {
	t.Helper()
	var rec agentstore.Record
	found := pollUntil(timeout, 50*time.Millisecond, func() bool {
		records, err := agentstore.Load(agentstore.FilePath(homePath))
		if err != nil {
			return false
		}
		r, ok := records[name]
		if !ok || r.SessionID == "" {
			return false
		}
		rec = r
		return true
	})
	return rec, found
}

// TestCodexAgentFlow spawns a codex-template agent through the real
// agent.Manager (backed by codexFakeSupervisor, see doc comment above),
// asserts the opening turn reached fakecodex with the prompt and the
// resulting thread id landed on the agentstore record (Harness: "codex"),
// then delivers a second message through the real web message endpoint and
// asserts fakecodex saw `resume <id> <msg>`. Finally it flips to the
// stale_resume scenario and asserts the driver's one-step retry ladder:
// the resumed call fails empty, and the retry drops `resume` entirely.
func TestCodexAgentFlow(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "state"), 0o750); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}

	argLog1 := filepath.Join(t.TempDir(), "turn1.json")
	t.Setenv("PATH", fakeBinDir()+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKECODEX_SCENARIO", "success")
	t.Setenv("FAKECODEX_ARGLOG", argLog1)
	t.Setenv("FAKECODEX_ENVLOG", "")

	port := freeLocalPort(t)
	cfg := &config.Config{
		HomePath: ws,
		Web:      config.WebConfig{Enabled: true, Port: port, Bind: "127.0.0.1"},
		Templates: map[string]config.TemplateConfig{
			"codextpl": {
				Workspace:      ws,
				Harness:        "codex",
				Model:          "gpt-5.3-codex",
				HarnessOptions: map[string]any{"sandbox": "workspace-write"},
			},
		},
	}
	cfgLoader := func() (*config.Config, error) { return cfg, nil }

	sup := newCodexFakeSupervisor(ws)
	mgr := agent.New(cfgLoader, sup, "", "")

	const openingPrompt = "Reply with exactly: pong"
	rec, err := mgr.Spawn(context.Background(), agent.SpawnSpec{
		Template: "codextpl",
		Repo:     "work",
		Prompt:   openingPrompt,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	agentName := rec.Name

	if !pollUntil(5*time.Second, 50*time.Millisecond, func() bool {
		_, err := os.Stat(argLog1)
		return err == nil
	}) {
		t.Fatal("fakecodex was never invoked for the opening turn (no arg log written)")
	}
	args1 := readArgLog(t, argLog1)
	want1 := []string{
		"exec", "--json", "--skip-git-repo-check",
		"--model", "gpt-5.3-codex",
		"--sandbox", "workspace-write",
		openingPrompt,
	}
	if strings.Join(args1, "\x00") != strings.Join(want1, "\x00") {
		t.Errorf("opening turn args = %#v, want %#v", args1, want1)
	}

	agentRec, found := readAgentRecord(t, ws, agentName, 5*time.Second)
	if !found {
		t.Fatal("agentstore never recorded a non-empty thread id for the spawned codex agent")
	}
	if agentRec.Harness != "codex" {
		t.Errorf("agentstore record Harness = %q, want %q", agentRec.Harness, "codex")
	}
	if agentRec.SessionID != "thread_fake_1" {
		t.Errorf("agentstore record SessionID = %q, want %q", agentRec.SessionID, "thread_fake_1")
	}

	// --- second message via the real web message endpoint ---
	apiToken, err := web.EnsureAPIToken(cfg.StatePath())
	if err != nil {
		t.Fatalf("EnsureAPIToken: %v", err)
	}
	srv := daemon.New(filepath.Join(ws, "state", "leo.sock"), "", nil)
	if err := srv.StartWeb(cfg, mgr); err != nil {
		t.Fatalf("StartWeb: %v", err)
	}
	t.Cleanup(func() { _ = srv.Shutdown() })

	argLog2 := filepath.Join(t.TempDir(), "turn2.json")
	t.Setenv("FAKECODEX_ARGLOG", argLog2)

	postMessage(t, port, apiToken, agentName, "second turn message")

	if !pollUntil(5*time.Second, 50*time.Millisecond, func() bool {
		_, err := os.Stat(argLog2)
		return err == nil
	}) {
		t.Fatal("fakecodex was never invoked for the resumed turn")
	}
	args2 := readArgLog(t, argLog2)
	joined2 := strings.Join(args2, " ")
	if !strings.Contains(joined2, "resume thread_fake_1 second turn message") {
		t.Errorf("resumed turn args = %v, want to contain %q", args2, "resume thread_fake_1 second turn message")
	}

	// --- stale_resume: the resumed call fails empty, retry drops `resume` ---
	argLog3 := filepath.Join(t.TempDir(), "turn3.json")
	t.Setenv("FAKECODEX_SCENARIO", "stale_resume")
	t.Setenv("FAKECODEX_ARGLOG", argLog3)

	postMessage(t, port, apiToken, agentName, "third turn after stale thread")

	if !pollUntil(5*time.Second, 50*time.Millisecond, func() bool {
		_, err := os.Stat(argLog3)
		return err == nil
	}) {
		t.Fatal("fakecodex was never invoked for the stale-resume retry")
	}
	args3 := readArgLog(t, argLog3)
	joined3 := strings.Join(args3, " ")
	if strings.Contains(joined3, "resume") {
		t.Errorf("final (retried) turn args = %v, must NOT contain resume — the one-step retry ladder should have dropped it", args3)
	}
	if !strings.Contains(joined3, "third turn after stale thread") {
		t.Errorf("final turn args = %v, want to contain the retried message", args3)
	}

	finalRec, found := readAgentRecord(t, ws, agentName, 5*time.Second)
	if !found {
		t.Fatal("agentstore record missing after stale-resume retry")
	}
	if finalRec.SessionID != "thread_fake_1" {
		t.Errorf("agentstore SessionID after stale-resume retry = %q, want %q (fakecodex's fresh-turn default)", finalRec.SessionID, "thread_fake_1")
	}
}

// postMessage POSTs a JSON {"text": msg} body to
// /web/process/{name}/message on the given port, authenticated with a
// bearer token, and fails the test on a non-200 response.
func postMessage(t *testing.T, port int, apiToken, name, msg string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"text": msg})
	if err != nil {
		t.Fatalf("marshaling message body: %v", err)
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/web/process/%s/message", port, name)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("posting message: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("message endpoint returned %d for %q", resp.StatusCode, msg)
	}
}

// --- opencode agent flow (driver-level, no tmux/resident serve pane) ---

// stringIDStore is a trivial in-memory harness.SessionIDStore for driver
// calls that don't need agentstore/session-store plumbing.
type stringIDStore struct {
	mu sync.Mutex
	id string
}

func (s *stringIDStore) Get() string   { s.mu.Lock(); defer s.mu.Unlock(); return s.id }
func (s *stringIDStore) Set(id string) { s.mu.Lock(); defer s.mu.Unlock(); s.id = id }
func (s *stringIDStore) Clear()        { s.mu.Lock(); defer s.mu.Unlock(); s.id = "" }

// TestOpencodeAgentFlow drives opencodeharness.ServerDriver directly against
// fakeopencode: provisioning (EnsureServerState creates the state file),
// message delivery (`run --attach` argv incl. --dir, plus the server
// password in env), and the session-list fallback when the attach stream
// never surfaces a sessionID. The resident `opencode serve` pane itself
// needs real tmux (out of e2e scope — see TestRealHarnessSmokeOpencode).
func TestOpencodeAgentFlow(t *testing.T) {
	ws := t.TempDir()
	home := t.TempDir()
	t.Setenv("PATH", fakeBinDir()+string(os.PathListSeparator)+os.Getenv("PATH"))

	const tmuxSession = "leo-oc-worker"
	const model = "anthropic/claude-sonnet-4-5"

	state, err := opencodeharness.EnsureServerState(home, tmuxSession, model)
	if err != nil {
		t.Fatalf("EnsureServerState: %v", err)
	}
	statePath := filepath.Join(home, "state", "opencode", tmuxSession+".json")
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("expected provisioning file at %s: %v", statePath, err)
	}
	if state.Port <= 0 {
		t.Errorf("provisioned port = %d, want > 0", state.Port)
	}
	if state.Password == "" {
		t.Error("provisioned password is empty")
	}

	argLog := filepath.Join(t.TempDir(), "args.json")
	envLog := filepath.Join(t.TempDir(), "env.json")
	t.Setenv("FAKEOPENCODE_SCENARIO", "success")
	t.Setenv("FAKEOPENCODE_ARGLOG", argLog)
	t.Setenv("FAKEOPENCODE_ENVLOG", envLog)

	ids := &stringIDStore{}
	handle := harness.SessionHandle{
		Kind:        harness.KindAgent,
		Name:        "oc-worker",
		TmuxSession: tmuxSession,
		Workspace:   ws,
		HomePath:    home,
		IDs:         ids,
	}
	drv := opencodeharness.ServerDriver{}

	res, err := drv.Inject(context.Background(), handle, "hello codex")
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}
	if res.Text != "fake opencode done" {
		t.Errorf("result text = %q, want %q", res.Text, "fake opencode done")
	}

	args := readArgLog(t, argLog)
	want := []string{
		"run", "--attach", state.URL(), "--format", "json", "--dir", ws,
		"--model", model,
		"hello codex",
	}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("run --attach args = %#v, want %#v", args, want)
	}

	envMap := readEnvLog(t, envLog)
	if got := envMap["OPENCODE_SERVER_PASSWORD"]; got != state.Password {
		t.Errorf("OPENCODE_SERVER_PASSWORD = %q, want %q", got, state.Password)
	}

	if ids.Get() != "ses_fake000000000000000000001" {
		t.Errorf("stored session id = %q, want %q", ids.Get(), "ses_fake000000000000000000001")
	}

	// --- second message resumes via -s ---
	argLog2 := filepath.Join(t.TempDir(), "args2.json")
	t.Setenv("FAKEOPENCODE_ARGLOG", argLog2)
	if _, err := drv.Inject(context.Background(), handle, "second message"); err != nil {
		t.Fatalf("Inject (resume): %v", err)
	}
	args2 := readArgLog(t, argLog2)
	joined2 := strings.Join(args2, " ")
	if !strings.Contains(joined2, "-s ses_fake000000000000000000001") {
		t.Errorf("resumed args = %v, want to contain -s ses_fake000000000000000000001", args2)
	}

	// --- session-list fallback: the attach stream never surfaces a
	// sessionID, forcing ServerDriver to shell out to `session list`.
	ids.Clear()
	t.Setenv("FAKEOPENCODE_SCENARIO", "no_session_id")
	argLog3 := filepath.Join(t.TempDir(), "args3.json")
	t.Setenv("FAKEOPENCODE_ARGLOG", argLog3)

	res3, err := drv.Inject(context.Background(), handle, "third message")
	if err != nil {
		t.Fatalf("Inject (no_session_id): %v", err)
	}
	if res3.SessionID == "" {
		t.Error("expected the session-list fallback to populate a session id")
	}
	if ids.Get() == "" {
		t.Error("expected the fallback session id to be persisted via IDs.Set")
	}
}

// --- persistent codex session sync completion (real harness-aware injector) ---

// TestCodexPersistentSessionRealInjector drives a codex persistent task
// through the REAL harness-aware injector (service.BuildSessionDispatch +
// the codex TurnDriver), not the mock injector
// TestPersistentRuntimeNonClaudeSyncCompletion uses — this test's fake is
// only the codex BINARY (fakecodex on PATH), not leo's own dispatch code.
// No tmux is involved: TurnDriver spawns a one-shot process per turn.
func TestCodexPersistentSessionRealInjector(t *testing.T) {
	dir := mkTempE2EDir(t, "leo-e2e-persist-codex-real-*")

	t.Setenv("PATH", fakeBinDir()+string(os.PathListSeparator)+os.Getenv("PATH"))
	argLog := filepath.Join(t.TempDir(), "args.json")
	t.Setenv("FAKECODEX_SCENARIO", "success")
	t.Setenv("FAKECODEX_ARGLOG", argLog)

	cfgYAML := fmt.Sprintf(`defaults:
  max_turns: 15
tasks:
  nightly:
    workspace: %s
    schedule: "0 9 * * *"
    prompt_file: prompts/NIGHTLY.md
    runtime: persistent
    harness: codex
    model: gpt-5.3-codex
    enabled: true
`, dir)
	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatalf("writing leo.yaml: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	const promptBody = "Run the nightly build."
	if err := os.WriteFile(filepath.Join(dir, "prompts/NIGHTLY.md"), []byte(promptBody+"\n"), 0o644); err != nil {
		t.Fatalf("writing prompt: %v", err)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}

	specs, err := service.SessionSpecsFromConfig(cfg)
	if err != nil {
		t.Fatalf("SessionSpecsFromConfig: %v", err)
	}
	dispatch := service.BuildSessionDispatch(specs, dir, cfg, "")

	srv := startDaemon(t, dir, cfgPath)
	srv.SetInjector(func(ctx context.Context, tmuxSession, prompt string) (*harness.Result, error) {
		d, ok := dispatch[tmuxSession]
		if !ok {
			return nil, fmt.Errorf("no dispatch entry for tmux session %q", tmuxSession)
		}
		h, err := harness.Get(d.Harness)
		if err != nil {
			return nil, err
		}
		return h.Driver().Inject(ctx, d.Handle, prompt)
	})
	srv.SetAborter(func(string) error { return nil })

	stdout, stderr, code := runLeo(t, dir, nil, "run", "nightly", "-c", cfgPath)
	if code != 0 {
		t.Fatalf("leo run exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	entry := pollHistoryEntry(t, dir, "nightly", 0, 3*time.Second)
	if entry.Reason != history.ReasonSuccess {
		t.Errorf("history reason = %q, want %q", entry.Reason, history.ReasonSuccess)
	}

	got := pollStoredSessionID(t, dir, "nightly", 3*time.Second)
	if got != "thread_fake_1" {
		t.Errorf("stored session id = %q, want %q", got, "thread_fake_1")
	}

	if !pollUntil(3*time.Second, 50*time.Millisecond, func() bool {
		_, err := os.Stat(argLog)
		return err == nil
	}) {
		t.Fatal("fakecodex was never invoked by the real injector")
	}
	args := readArgLog(t, argLog)
	want := []string{
		"exec", "--json", "--skip-git-repo-check",
		"--model", "gpt-5.3-codex",
		promptBody + "\n",
	}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("injected turn args = %#v, want %#v", args, want)
	}

	// history.Reason == Success without ever calling daemon.ReportTask
	// confirms the sync-completion path: a DriveTurns driver's Inject
	// returns a non-nil *harness.Result directly, so the daemon never
	// waits on an async Stop-hook-style report.
}
