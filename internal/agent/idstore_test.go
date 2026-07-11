package agent

import (
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
)

func TestAgentIDsGetSetClear(t *testing.T) {
	home := t.TempDir()
	if err := agentstore.Save(home, agentstore.Record{
		Name:      "leo-foo",
		Workspace: "/tmp/ws",
		SpawnedAt: time.Now(),
	}); err != nil {
		t.Fatalf("agentstore.Save: %v", err)
	}

	ids := NewAgentIDs(home, "leo-foo")
	if got := ids.Get(); got != "" {
		t.Fatalf("Get() before Set = %q, want empty", got)
	}

	ids.Set("thread-123")
	if got := ids.Get(); got != "thread-123" {
		t.Fatalf("Get() after Set = %q, want thread-123", got)
	}

	records, err := agentstore.Load(agentstore.FilePath(home))
	if err != nil {
		t.Fatalf("agentstore.Load: %v", err)
	}
	if records["leo-foo"].SessionID != "thread-123" {
		t.Fatalf("record.SessionID = %q, want thread-123", records["leo-foo"].SessionID)
	}

	ids.Clear()
	if got := ids.Get(); got != "" {
		t.Fatalf("Get() after Clear = %q, want empty", got)
	}
}

func TestAgentIDsMissingRecordIsNoOp(t *testing.T) {
	home := t.TempDir()
	ids := NewAgentIDs(home, "leo-ghost")

	if got := ids.Get(); got != "" {
		t.Fatalf("Get() on missing record = %q, want empty", got)
	}
	// Set/Clear on a missing record must not panic and must not create one.
	ids.Set("whatever")
	ids.Clear()

	if _, err := agentstore.Load(agentstore.FilePath(home)); err == nil {
		t.Fatalf("expected no agentstore file to be created for a missing record")
	}
}
