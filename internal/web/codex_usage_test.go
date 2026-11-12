package web

import (
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/observe"
)

type fixedIDs string

func (f fixedIDs) Get() string { return string(f) }
func (fixedIDs) Set(string)    {}
func (fixedIDs) Clear()        {}

func TestCodexUsageLister(t *testing.T) {
	procStart := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	staleStart := procStart.Add(-48 * time.Hour)
	records := []agent.Record{
		{Name: "cx-id", Status: "running", Workspace: "/ws/a", StartedAt: staleStart},
		{Name: "cx-noid", Status: "running", Workspace: "/ws/b", StartedAt: procStart},
		{Name: "cx-stopped", Status: "stopped", Workspace: "/ws/c"},
		{Name: "claude-one", Status: "running", Workspace: "/ws/d"},
		{Name: "cx-unresolved", Status: "running", Workspace: "/ws/e"},
	}
	handles := map[string]struct {
		harness string
		h       harness.SessionHandle
	}{
		"cx-id":      {"codex", harness.SessionHandle{Workspace: "/ws/a", Env: map[string]string{"CODEX_HOME": "/codex/a"}, IDs: fixedIDs("sid-1")}},
		"cx-noid":    {"codex", harness.SessionHandle{Workspace: "/ws/b", Env: map[string]string{"CODEX_HOME": "/codex/b"}, IDs: fixedIDs("")}},
		"cx-stopped": {"codex", harness.SessionHandle{Workspace: "/ws/c"}},
		"claude-one": {"claude", harness.SessionHandle{Workspace: "/ws/d"}},
	}
	l := codexUsageLister{
		list: func() []agent.Record { return records },
		// The supervisor's start time wins over the record's stale one.
		states: func() map[string]ProcessStateInfo {
			return map[string]ProcessStateInfo{"cx-id": {Status: "running", StartedAt: procStart}}
		},
		resolve: func(name string) (string, harness.SessionHandle, bool) {
			r, ok := handles[name]
			return r.harness, r.h, ok
		},
	}

	got := l.CodexAgents()

	want := []observe.CodexAgent{
		{Name: "cx-id", Home: "/codex/a", SessionID: "sid-1", Workspace: "/ws/a", StartedAt: procStart},
		{Name: "cx-noid", Home: "/codex/b", SessionID: "", Workspace: "/ws/b", StartedAt: procStart},
	}
	if len(got) != len(want) {
		t.Fatalf("agents = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("agent %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
