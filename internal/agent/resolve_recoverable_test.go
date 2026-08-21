package agent

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
)

// TestResolveRecoverable exercises the real implementation directly rather
// than a test double, locking the exact filter that used to only live behind
// a fake in the daemon handler tests. A reviewer flagged that gap: dropping
// `|| rec.StoppedReason == ""` from the real filter would leave the daemon
// test suite green (its fake never runs the real code) while making every
// user-stopped worktree record restartable/stoppable via the recovery
// fallback — the opposite of the guarantee ResolveRecoverable's doc comment
// makes.
func TestResolveRecoverable(t *testing.T) {
	home := t.TempDir()
	_ = agentstore.Save(home, agentstore.Record{
		Name:          "leo-failed-restore",
		Workspace:     "/w",
		Stopped:       true,
		StoppedReason: "workspace missing: /w",
	})
	_ = agentstore.Save(home, agentstore.Record{
		Name:      "leo-user-stopped",
		Workspace: "/w",
		Stopped:   true,
		// StoppedReason deliberately empty: a user-initiated `leo agent
		// stop`, not a system-marked failed restore.
	})

	sup := &fakeSupervisor{ephemeral: map[string]ProcessState{
		"leo-live": {Name: "leo-live", Status: "running"},
	}}
	m := newTestManager(t, home, sup)

	t.Run("reason-set record matches", func(t *testing.T) {
		rec, ok := m.ResolveRecoverable("leo-failed-restore")
		if !ok {
			t.Fatal("expected ok=true for a Stopped record with a non-empty StoppedReason")
		}
		if rec.Name != "leo-failed-restore" {
			t.Errorf("Name = %q", rec.Name)
		}
	})

	t.Run("user-stopped record with no reason does not match", func(t *testing.T) {
		if _, ok := m.ResolveRecoverable("leo-user-stopped"); ok {
			t.Fatal("expected ok=false for a Stopped record with an empty StoppedReason (user-stopped)")
		}
	})

	t.Run("live agent does not match", func(t *testing.T) {
		if _, ok := m.ResolveRecoverable("leo-live"); ok {
			t.Fatal("expected ok=false for a live agent name — nothing to recover")
		}
	})

	t.Run("unknown name does not match", func(t *testing.T) {
		if _, ok := m.ResolveRecoverable("leo-ghost"); ok {
			t.Fatal("expected ok=false for a name with no agentstore record")
		}
	})
}
