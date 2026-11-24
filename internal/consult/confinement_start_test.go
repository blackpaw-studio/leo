package consult

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

type fakeWorkspaces map[string]CallerHome

func (f fakeWorkspaces) CallerWorkspace(_ *config.Config, caller string) (CallerHome, bool) {
	h, ok := f[caller]
	return h, ok
}

// confinementHarness is a dispatcher with a file recorder (so refusals can be
// shown not to record), a counting git seam, and two callers' workspaces.
type confinementHarness struct {
	d              *Dispatcher
	stateDir       string
	leoWS, otherWS string
	mu             sync.Mutex
	gitCalls       [][]string
	started        []string
}

func newConfinementHarness(t *testing.T) *confinementHarness {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &confinementHarness{stateDir: filepath.Join(root, "state"), leoWS: filepath.Join(root, "agents", "leo"), otherWS: filepath.Join(root, "agents", "marketing")}
	for _, dir := range []string{h.stateDir, h.leoWS, h.otherWS} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel and wait for the sleeping children, so none writes its record
	// after the temp dir is removed.
	t.Cleanup(func() {
		cancel()
		h.d.Wait(context.Background(), h.started, 10*time.Second)
	})
	h.d = NewDispatcher(NewFileRecorder(h.stateDir), ctx)
	h.d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "30")
	}
	h.d.GitCommand = func(name string, args ...string) *exec.Cmd {
		h.mu.Lock()
		h.gitCalls = append(h.gitCalls, args)
		h.mu.Unlock()
		return exec.Command("sh", "-c", "exit 1")
	}
	h.d.SetCallerWorkspaces(fakeWorkspaces{
		"leo":       {Workspace: h.leoWS, Harness: "claude"},
		"inherited": {Workspace: h.otherWS, Harness: "claude"},
	})
	return h
}

func confinementConfig(mode string) *config.Config {
	cfg := testConfig()
	cfg.Defaults.DispatchConfinement = mode
	cfg.APIClients = map[string]config.APIClientConfig{"leoterm-ios": {}}
	return cfg
}

func (h *confinementHarness) start(t *testing.T, mode string, req Request) (Started, error) {
	t.Helper()
	req.Template, req.Prompt = "claude", "q"
	if req.Kind == "" {
		req.Kind = "dispatch"
	}
	started, err := h.d.Start(context.Background(), confinementConfig(mode), req)
	if err == nil {
		h.started = append(h.started, started.ID)
	}
	return started, err
}

func (h *confinementHarness) recorded(t *testing.T) int {
	t.Helper()
	recs, err := Load(h.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	return len(recs)
}

func hasWarning(ws []string, sub string) bool {
	for _, w := range ws {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

func TestStartConfinementWarnProceedsAndRecordsTheWarning(t *testing.T) {
	h := newConfinementHarness(t)
	started, err := h.start(t, "", Request{Caller: "leo", Cwd: h.otherWS}) // empty mode = warn
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !hasWarning(started.Warnings, "dispatch_confinement=enforce") || !hasWarning(started.Warnings, h.otherWS) {
		t.Fatalf("warnings = %v, want one naming the cwd and the enforce mode", started.Warnings)
	}
	rec, err := h.d.Get(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarning(rec.Warnings, "dispatch_confinement=enforce") {
		t.Fatalf("record warnings = %v, want the confinement warning persisted", rec.Warnings)
	}
}

func TestStartConfinementEnforceRefusesWithoutRecording(t *testing.T) {
	h := newConfinementHarness(t)
	_, err := h.start(t, "enforce", Request{Caller: "leo", Cwd: h.otherWS})
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want a ValidationError", err)
	}
	for _, want := range []string{h.otherWS, h.leoWS, `"leo"`, "allowed roots"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err %q lacks %q", err, want)
		}
	}
	if n := h.recorded(t); n != 0 {
		t.Fatalf("%d records written for a refused dispatch", n)
	}
}

func TestStartConfinementAllowsTheWorkspaceInEnforce(t *testing.T) {
	h := newConfinementHarness(t)
	started, err := h.start(t, "enforce", Request{Caller: "leo", Cwd: h.leoWS})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if hasWarning(started.Warnings, "dispatch_confinement") {
		t.Fatalf("warnings = %v for an allowed cwd", started.Warnings)
	}
}

func TestStartConfinementOffNeitherWarnsNorProbesGit(t *testing.T) {
	h := newConfinementHarness(t)
	started, err := h.start(t, "off", Request{Caller: "leo", Cwd: h.otherWS})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if hasWarning(started.Warnings, "dispatch_confinement") {
		t.Fatalf("warnings = %v in off mode", started.Warnings)
	}
	if len(h.gitCalls) != 0 {
		t.Fatalf("off mode ran git: %v", h.gitCalls)
	}
}

func TestStartConfinementNestedDispatchIsJudgedByTheRootCaller(t *testing.T) {
	h := newConfinementHarness(t)
	root, err := h.start(t, "enforce", Request{Caller: "leo", Cwd: h.leoWS})
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	// The child's own caller resolves to the other workspace; the root's must win.
	if _, err := h.start(t, "enforce", Request{Caller: "inherited", ParentDispatchID: root.ID, Cwd: h.otherWS}); err == nil {
		t.Fatal("nested dispatch into another workspace allowed because its inherited caller owns it")
	}
	if _, err := h.start(t, "enforce", Request{Caller: "inherited", ParentDispatchID: root.ID, Cwd: h.leoWS}); err != nil {
		t.Fatalf("nested dispatch inside the root's workspace refused: %v", err)
	}
}

func TestStartConfinementExemptCallers(t *testing.T) {
	h := newConfinementHarness(t)
	cliRoot, err := h.start(t, "enforce", Request{Caller: "", Cwd: h.otherWS})
	if err != nil {
		t.Fatalf("empty caller (human CLI) refused: %v", err)
	}
	if _, err := h.start(t, "enforce", Request{Caller: "somebody", ParentDispatchID: cliRoot.ID, Cwd: h.leoWS}); err != nil {
		t.Fatalf("nested under a CLI root refused: %v", err)
	}
	if _, err := h.start(t, "enforce", Request{Caller: "leoterm-ios", Cwd: h.otherWS}); err != nil {
		t.Fatalf("api_clients caller refused: %v", err)
	}
}

func TestStartConfinementUnresolvableNamedCaller(t *testing.T) {
	h := newConfinementHarness(t)
	started, err := h.start(t, "warn", Request{Caller: "ghost", Cwd: h.leoWS})
	if err != nil {
		t.Fatalf("warn: %v", err)
	}
	if !hasWarning(started.Warnings, `"ghost"`) {
		t.Fatalf("warnings = %v, want one naming the unresolved caller", started.Warnings)
	}
	_, err = h.start(t, "enforce", Request{Caller: "ghost", Cwd: h.leoWS})
	var verr *ValidationError
	if !errors.As(err, &verr) || !strings.Contains(err.Error(), `"ghost"`) {
		t.Fatalf("enforce err = %v, want a refusal naming the caller", err)
	}
}

func TestStartConfinementDoesNotCheckConsults(t *testing.T) {
	h := newConfinementHarness(t)
	started, err := h.start(t, "enforce", Request{Kind: "consult", Caller: "leo", Cwd: h.otherWS})
	if err != nil {
		t.Fatalf("consult refused: %v", err)
	}
	if hasWarning(started.Warnings, "dispatch_confinement") {
		t.Fatalf("warnings = %v for a consult", started.Warnings)
	}
}

func TestStartConfinementJudgesTheSourceCwdBeforeCreatingAWorktree(t *testing.T) {
	h := newConfinementHarness(t)
	_, err := h.start(t, "enforce", Request{Caller: "leo", Cwd: h.otherWS, Isolation: "worktree"})
	if err == nil || !strings.Contains(err.Error(), "allowed roots") {
		t.Fatalf("err = %v, want a confinement refusal of the source cwd", err)
	}
	for _, c := range h.gitCalls {
		if strings.Contains(strings.Join(c, " "), "worktree") {
			t.Fatalf("worktree prepared before the check: %v", c)
		}
	}
	// A source cwd inside the workspace passes the check; the fake git then
	// fails the repository probe, which is not a confinement refusal.
	_, err = h.start(t, "enforce", Request{Caller: "leo", Cwd: h.leoWS, Isolation: "worktree"})
	if err == nil || strings.Contains(err.Error(), "allowed roots") {
		t.Fatalf("err = %v, want the worktree preparation error", err)
	}
}
