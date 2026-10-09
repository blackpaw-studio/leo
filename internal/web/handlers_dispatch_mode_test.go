package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/consult"
)

// launchRecorder is the interactive runtime seam: it records every Launch so
// tests assert what the daemon actually asked tmux for.
type launchRecorder struct {
	mu       sync.Mutex
	launches []consult.LaunchRequest
	err      error
}

func (r *launchRecorder) Launch(_ context.Context, req consult.LaunchRequest) (string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.launches = append(r.launches, req)
	if r.err != nil {
		return "", "", r.err
	}
	return "%9", "dispatch", nil
}
func (r *launchRecorder) Inject(context.Context, string, string, func() error) error { return nil }
func (r *launchRecorder) Alive(string) bool                                          { return true }
func (r *launchRecorder) Kill(string) error                                          { return nil }
func (r *launchRecorder) ComposerEmpty(string) bool                                  { return true }

func (r *launchRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.launches)
}

type startedPayload struct {
	Data struct {
		ID   string       `json:"id"`
		Mode consult.Mode `json:"mode"`
		Note string       `json:"note"`
	} `json:"data"`
}

func postDispatch(t *testing.T, s *Server, body string) startedPayload {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAPIDispatch(w, httptest.NewRequest("POST", "/api/dispatch", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("dispatch: %d %s", w.Code, w.Body.String())
	}
	var out startedPayload
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func newModeTestServer(t *testing.T) (*Server, *launchRecorder, string) {
	t.Helper()
	s, dir, _ := newTestServerWithAgents(t)
	rt := &launchRecorder{}
	s.consults.SetInteractiveRuntime(rt)
	s.consults.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"done","is_error":false}`)
	}
	prev := locateTmux
	locateTmux = func() (string, error) { return "/usr/bin/tmux", nil }
	t.Cleanup(func() { locateTmux = prev })
	return s, rt, dir
}

func TestAPIDispatchOmittedModeIsInteractiveAndFallsBackWithoutCallerPane(t *testing.T) {
	s, rt, _ := newModeTestServer(t)
	got := postDispatch(t, s, `{"from":"nightly-task","template":"coding","prompt":"work","cwd":"/tmp"}`)
	if got.Data.Mode != consult.ModeInteractive || got.Data.Note != "" {
		t.Fatalf("mode=%q note=%q, want interactive with no note", got.Data.Mode, got.Data.Note)
	}
	if rt.count() != 1 {
		t.Fatalf("launches=%d, want 1", rt.count())
	}
	launch := rt.launches[0]
	if launch.CallerPaneID != "" || launch.CallerSessionID != "" {
		t.Fatalf("unresolvable caller must launch unplaced, got %+v", launch)
	}
	if launch.Placement.Target != "" || launch.Placement.Kind != "window" {
		t.Fatalf("placement=%+v, want a standalone window", launch.Placement)
	}
}

func TestAPIDispatchExplicitHeadlessStaysHeadless(t *testing.T) {
	s, rt, _ := newModeTestServer(t)
	got := postDispatch(t, s, `{"template":"coding","prompt":"work","cwd":"/tmp","mode":"headless"}`)
	if got.Data.Mode != consult.ModeHeadless || got.Data.Note != "" {
		t.Fatalf("mode=%q note=%q", got.Data.Mode, got.Data.Note)
	}
	if rt.count() != 0 {
		t.Fatalf("headless dispatch launched %d panes", rt.count())
	}
}

func TestAPIDispatchOmittedModeFallsBackToHeadlessWithoutTmux(t *testing.T) {
	s, rt, _ := newModeTestServer(t)
	locateTmux = func() (string, error) { return "", os.ErrNotExist }
	got := postDispatch(t, s, `{"template":"coding","prompt":"work","cwd":"/tmp"}`)
	if got.Data.Mode != consult.ModeHeadless || !strings.Contains(got.Data.Note, "tmux") {
		t.Fatalf("mode=%q note=%q, want headless explaining tmux", got.Data.Mode, got.Data.Note)
	}
	if rt.count() != 0 {
		t.Fatalf("launched %d panes without tmux", rt.count())
	}
}

func TestAPIDispatchOmittedModeFallsBackToHeadlessForOpencode(t *testing.T) {
	s, rt, dir := newModeTestServer(t)
	cfg := testConfigWithTemplatesYAML + "  local:\n    harness: opencode\n    model: lmstudio/qwen\n"
	if err := os.WriteFile(filepath.Join(dir, "leo.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	got := postDispatch(t, s, `{"template":"local","prompt":"work","cwd":"/tmp"}`)
	if got.Data.Mode != consult.ModeHeadless || !strings.Contains(got.Data.Note, "opencode") {
		t.Fatalf("mode=%q note=%q, want headless explaining opencode", got.Data.Mode, got.Data.Note)
	}
	if rt.count() != 0 {
		t.Fatalf("launched %d panes for opencode", rt.count())
	}
}

func TestAPIDispatchExplicitInteractiveOnOpencodeStillErrors(t *testing.T) {
	s, _, dir := newModeTestServer(t)
	cfg := testConfigWithTemplatesYAML + "  local:\n    harness: opencode\n    model: lmstudio/qwen\n"
	if err := os.WriteFile(filepath.Join(dir, "leo.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.handleAPIDispatch(w, httptest.NewRequest("POST", "/api/dispatch", strings.NewReader(`{"template":"local","prompt":"work","cwd":"/tmp","mode":"interactive"}`)))
	if w.Code != 400 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestAPIDispatchOmittedModeFallsBackToHeadlessWhenInteractiveLaunchFails(t *testing.T) {
	s, rt, _ := newModeTestServer(t)
	rt.err = errors.New("no server running on /tmp/tmux-0/leo")
	got := postDispatch(t, s, `{"template":"coding","prompt":"work","cwd":"/tmp"}`)
	if got.Data.Mode != consult.ModeHeadless || !strings.Contains(got.Data.Note, "interactive launch failed") || !strings.Contains(got.Data.Note, "no server running") {
		t.Fatalf("mode=%q note=%q, want headless explaining the launch failure", got.Data.Mode, got.Data.Note)
	}
	if rt.count() != 1 {
		t.Fatalf("launches=%d, want the single failed interactive attempt", rt.count())
	}
}

func TestAPIDispatchExplicitInteractiveLaunchFailureStillErrors(t *testing.T) {
	s, rt, _ := newModeTestServer(t)
	rt.err = errors.New("no server running")
	w := httptest.NewRecorder()
	s.handleAPIDispatch(w, httptest.NewRequest("POST", "/api/dispatch", strings.NewReader(`{"template":"coding","prompt":"work","cwd":"/tmp","mode":"interactive"}`)))
	if w.Code != 500 || !strings.Contains(w.Body.String(), "no server running") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

// The headless retry after a failed interactive launch is a fresh headless
// Start: its child must get the same daemon-backed leo MCP env and token, in
// process env only.
func TestAPIDispatchHeadlessFallbackChildGetsLeoMCPEnv(t *testing.T) {
	s, rt, _ := newModeTestServer(t)
	rt.err = errors.New("no server running")
	s.consults.AgentToken = "fallback-token"
	var captured *exec.Cmd
	var argv []string
	s.consults.ExecCommandContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		argv = args
		captured = exec.CommandContext(ctx, "echo", `{"type":"result","result":"done","is_error":false}`)
		return captured
	}
	got := postDispatch(t, s, `{"template":"coding","prompt":"work","cwd":"/tmp"}`)
	if got.Data.Mode != consult.ModeHeadless {
		t.Fatalf("mode=%q, want the headless fallback", got.Data.Mode)
	}
	s.consults.Wait(context.Background(), []string{got.Data.ID}, 5*time.Second)
	env := map[string]string{}
	for _, entry := range captured.Env {
		if k, v, ok := strings.Cut(entry, "="); ok {
			env[k] = v
		}
	}
	if env["LEO_API_TOKEN"] != "fallback-token" || env["LEO_WEB_PORT"] != "8370" || env["LEO_DISPATCH_ID"] != got.Data.ID || env["LEO_PROCESS_NAME"] != "dispatch:"+got.Data.ID {
		t.Fatalf("fallback child env: token=%q port=%q id=%q name=%q", env["LEO_API_TOKEN"], env["LEO_WEB_PORT"], env["LEO_DISPATCH_ID"], env["LEO_PROCESS_NAME"])
	}
	if strings.Contains(strings.Join(argv, "\x00"), "fallback-token") {
		t.Fatalf("token in argv: %q", argv)
	}
}
