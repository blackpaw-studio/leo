package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
)

// fakeTurnsDriver is a minimal harness.SessionDriver whose Attach returns a
// fixed AttachSpec so Manager.Logs / Manager.ResolveHandle can be exercised
// without a real driven process.
type fakeTurnsDriver struct {
	attachSpec harness.AttachSpec
	attachErr  error
	lastHandle harness.SessionHandle
}

func (d *fakeTurnsDriver) Style() harness.DriveStyle                          { return harness.DriveTurns }
func (d *fakeTurnsDriver) Start(context.Context, harness.SessionHandle) error { return nil }
func (d *fakeTurnsDriver) Inject(context.Context, harness.SessionHandle, string) (*harness.Result, error) {
	return nil, nil
}
func (d *fakeTurnsDriver) Attach(h harness.SessionHandle) (harness.AttachSpec, error) {
	d.lastHandle = h
	return d.attachSpec, d.attachErr
}

// fakeTurnsHarness is a minimal harness.Harness wrapping a fakeTurnsDriver.
type fakeTurnsHarness struct {
	name   string
	driver harness.SessionDriver
}

func (h fakeTurnsHarness) Name() string                              { return h.name }
func (h fakeTurnsHarness) Binary() string                            { return h.name }
func (h fakeTurnsHarness) Args(harness.LaunchSpec) ([]string, error) { return nil, nil }
func (h fakeTurnsHarness) SessionArgs(harness.SessionState) []string { return nil }
func (h fakeTurnsHarness) ValidateModel(string) error                { return nil }
func (h fakeTurnsHarness) DecodeOptions(map[string]any) (any, error) { return nil, nil }
func (h fakeTurnsHarness) SupportsChannels() bool                    { return false }
func (h fakeTurnsHarness) ParseEvents(io.Reader) (harness.Result, error) {
	return harness.Result{}, nil
}
func (h fakeTurnsHarness) Env(harness.LaunchSpec) (map[string]string, error) { return nil, nil }
func (h fakeTurnsHarness) SupportsKind(harness.Kind) bool                    { return true }
func (h fakeTurnsHarness) Driver() harness.SessionDriver                     { return h.driver }

const fakeTurnsHarnessName = "faketurns-agenttest"

var registerFakeTurnsHarnessOnce sync.Once

// registerFakeTurnsHarness registers fakeTurnsHarnessName once (the harness
// registry panics on duplicate registration) with the given driver.
func registerFakeTurnsHarness(drv harness.SessionDriver) {
	registerFakeTurnsHarnessOnce.Do(func() {
		harness.Register(fakeTurnsHarness{name: fakeTurnsHarnessName, driver: drv})
	})
}

// TestResolveHandleAgentstoreRecord verifies ResolveHandle builds a
// SessionHandle from the agentstore record and reports the record's harness.
func TestResolveHandleAgentstoreRecord(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-codex-worker",
		Harness:    "codex",
		Workspace:  "/tmp/codex-worker",
		ClaudeArgs: []string{"exec", "hello"},
		Env:        map[string]string{"FOO": "bar"},
	})
	m := newTestManager(t, home, &fakeSupervisor{})

	hn, h, ok := m.ResolveHandle("leo-codex-worker")
	if !ok {
		t.Fatal("expected ok=true for a known agentstore record")
	}
	if hn != "codex" {
		t.Fatalf("harness = %q, want %q", hn, "codex")
	}
	if h.Name != "leo-codex-worker" {
		t.Fatalf("handle.Name = %q", h.Name)
	}
	if h.TmuxSession != SessionName("leo-codex-worker") {
		t.Fatalf("handle.TmuxSession = %q", h.TmuxSession)
	}
	if h.Workspace != "/tmp/codex-worker" {
		t.Fatalf("handle.Workspace = %q", h.Workspace)
	}
	if h.Env["FOO"] != "bar" {
		t.Fatalf("handle.Env not propagated: %+v", h.Env)
	}
	if len(h.TurnArgs) != 2 || h.TurnArgs[0] != "exec" {
		t.Fatalf("handle.TurnArgs = %v", h.TurnArgs)
	}
	if h.IDs == nil {
		t.Fatal("handle.IDs must be set")
	}
}

// TestResolveHandleUnknownAgent verifies ok=false for a name with no
// agentstore record — the caller falls back to tmux/claude behavior.
func TestResolveHandleUnknownAgent(t *testing.T) {
	home := t.TempDir()
	m := newTestManager(t, home, &fakeSupervisor{})
	if _, _, ok := m.ResolveHandle("ghost"); ok {
		t.Fatal("expected ok=false for an unknown agent")
	}
}

// TestLogsDriveTurnsReadsHistoryFile verifies that Manager.Logs, for a
// record whose harness driver is DriveTurns, returns the tail of the
// driver's AttachSpec.HistoryPath instead of doing a tmux capture-pane.
func TestLogsDriveTurnsReadsHistoryFile(t *testing.T) {
	home := t.TempDir()
	historyPath := filepath.Join(home, "codex-worker.history")
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(historyPath, []byte(content), 0600); err != nil {
		t.Fatalf("writing history file: %v", err)
	}

	drv := &fakeTurnsDriver{attachSpec: harness.AttachSpec{HistoryPath: historyPath}}
	registerFakeTurnsHarness(drv)

	_ = agentstore.Save(home, agentstore.Record{
		Name:      "leo-codex-worker",
		Harness:   fakeTurnsHarnessName,
		Workspace: "/tmp/codex-worker",
	})
	sup := &fakeSupervisor{ephemeral: map[string]ProcessState{"leo-codex-worker": {Name: "leo-codex-worker", Status: "running"}}}
	m := newTestManager(t, home, sup)

	// lines <= 0 => whole file.
	got, err := m.Logs("leo-codex-worker", 0)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if got != content {
		t.Fatalf("Logs(0) = %q, want whole file %q", got, content)
	}

	// lines > 0 => tail.
	got, err = m.Logs("leo-codex-worker", 2)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	want := "line4\nline5"
	if got != want {
		t.Fatalf("Logs(2) = %q, want %q", got, want)
	}

	if drv.lastHandle.Name != "leo-codex-worker" {
		t.Fatalf("driver.Attach was not called with the right handle: %+v", drv.lastHandle)
	}
}

// TestLogsClaudeAgentUsesTmuxPath verifies a claude (or pre-Harness-field)
// record does NOT go through the DriveTurns history-file branch — Logs falls
// through to its existing tmux capture-pane logic, which fails fast here
// because there is no real tmux session, proving the DriveTurns branch was
// skipped rather than silently short-circuiting.
func TestLogsClaudeAgentUsesTmuxPath(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-claude-worker"})
	sup := &fakeSupervisor{ephemeral: map[string]ProcessState{"leo-claude-worker": {Name: "leo-claude-worker", Status: "running"}}}
	loader := func() (*config.Config, error) { return &config.Config{HomePath: home}, nil }
	m := New(loader, sup, "/definitely/not/a/real/tmux/path", "")

	_, err := m.Logs("leo-claude-worker", 0)
	if err == nil {
		t.Fatal("expected an error from the tmux capture-pane fallback path")
	}
}
