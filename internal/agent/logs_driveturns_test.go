package agent

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
)

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

// TestResolveHandleStoppedRecordReportsUnknown locks the fix for a reviewer-
// caught defect: ResolveHandle used to ignore Stopped entirely, so a
// non-claude (codex/opencode) record left Stopped — either by a user `leo
// agent stop` or by a failed boot-time restore (see markFailedRestore) —
// still reported ok=true. handleWebAgentMessage would then dispatch straight
// into a tmux session that no longer exists instead of falling through to a
// clean "no such agent" 404, the way the claude message path already does
// for the same record via its live-states check.
func TestResolveHandleStoppedRecordReportsUnknown(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{
		Name:          "leo-codex-doomed",
		Harness:       "codex",
		Workspace:     "/tmp/codex-doomed",
		ClaudeArgs:    []string{"exec", "hello"},
		Stopped:       true,
		StoppedReason: "workspace missing: /tmp/codex-doomed",
	})
	m := newTestManager(t, home, &fakeSupervisor{})

	if _, _, ok := m.ResolveHandle("leo-codex-doomed"); ok {
		t.Fatal("expected ok=false for a Stopped record — nothing live to deliver into")
	}
}

// TestLogsClaudeAgentUsesTmuxPath verifies a claude (or pre-Harness-field)
// record goes straight to the tmux capture-pane path — it fails fast here
// because there is no real tmux session at the given (bogus) path.
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

// TestLogsCodexAgentUsesTmuxPath verifies a codex-harness record ALSO goes
// straight to the tmux capture-pane path — there is no more DriveTurns
// transcript-file branch for any harness. It fails fast the same way the
// claude case does, proving no harness-specific short-circuit remains.
func TestLogsCodexAgentUsesTmuxPath(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{
		Name:      "leo-codex-worker2",
		Harness:   "codex",
		Workspace: "/tmp/codex-worker2",
	})
	sup := &fakeSupervisor{ephemeral: map[string]ProcessState{"leo-codex-worker2": {Name: "leo-codex-worker2", Status: "running"}}}
	loader := func() (*config.Config, error) { return &config.Config{HomePath: home}, nil }
	m := New(loader, sup, "/definitely/not/a/real/tmux/path", "")

	_, err := m.Logs("leo-codex-worker2", 0)
	if err == nil {
		t.Fatal("expected an error from the tmux capture-pane fallback path")
	}
}
