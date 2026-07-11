package service

import (
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
)

func TestNewStoreIDsGetSetClear(t *testing.T) {
	home := t.TempDir()
	ids := newStoreIDs(home, "process:foo")

	if got := ids.Get(); got != "" {
		t.Fatalf("Get() before Set = %q, want empty", got)
	}

	ids.Set("sess-1")
	if got := ids.Get(); got != "sess-1" {
		t.Fatalf("Get() after Set = %q, want sess-1", got)
	}

	ids.Clear()
	if got := ids.Get(); got != "" {
		t.Fatalf("Get() after Clear = %q, want empty", got)
	}
}

func TestNewStoreIDsIsolatedByKey(t *testing.T) {
	home := t.TempDir()
	a := newStoreIDs(home, "process:a")
	b := newStoreIDs(home, "process:b")

	a.Set("sess-a")
	if got := b.Get(); got != "" {
		t.Fatalf("b.Get() = %q, want empty (keys must not collide)", got)
	}
	if got := a.Get(); got != "sess-a" {
		t.Fatalf("a.Get() = %q, want sess-a", got)
	}
}

func TestAgentOrProcessIDsPicksAgentstoreForKnownAgent(t *testing.T) {
	home := t.TempDir()
	if err := agentstore.Save(home, agentstore.Record{
		Name:      "leo-agent1",
		Workspace: "/tmp/ws",
		SpawnedAt: time.Now(),
	}); err != nil {
		t.Fatalf("agentstore.Save: %v", err)
	}

	ids := agentOrProcessIDs(home, "leo-agent1")
	ids.Set("thread-abc")

	records, err := agentstore.Load(agentstore.FilePath(home))
	if err != nil {
		t.Fatalf("agentstore.Load: %v", err)
	}
	if records["leo-agent1"].SessionID != "thread-abc" {
		t.Fatalf("record.SessionID = %q, want thread-abc (Set must go through agentstore)", records["leo-agent1"].SessionID)
	}

	// Confirm it did NOT fall through to the session-store "process:" key.
	fallback := newStoreIDs(home, "process:leo-agent1")
	if got := fallback.Get(); got != "" {
		t.Fatalf("session-store fallback key was written to (%q); agentOrProcessIDs should have used the agentstore record", got)
	}
}

func TestAgentOrProcessIDsFallsBackToSessionStoreForUnknownName(t *testing.T) {
	home := t.TempDir()

	ids := agentOrProcessIDs(home, "some-config-process")
	ids.Set("sess-xyz")

	// The agentstore must remain untouched.
	if records, err := agentstore.Load(agentstore.FilePath(home)); err == nil && len(records) != 0 {
		t.Fatalf("expected no agentstore records, got %v", records)
	}

	fallback := newStoreIDs(home, "process:some-config-process")
	if got := fallback.Get(); got != "sess-xyz" {
		t.Fatalf("fallback session-store Get() = %q, want sess-xyz", got)
	}
}
