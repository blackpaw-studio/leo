package consult

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// clockedPresenceRuntime advances a fake clock inside PanePresence, standing
// in for a tmux call that takes a given time on a loaded host.
type clockedPresenceRuntime struct {
	*fakeInteractiveRuntime
	advance func(time.Duration)
	cost    map[string]time.Duration
}

func (r clockedPresenceRuntime) PanePresence(pane string) (PanePresence, error) {
	r.advance(r.cost[pane])
	return PaneAbsent, nil
}

func TestMarkInterruptedWarnsOnSlowRecord(t *testing.T) {
	var mu sync.Mutex
	clock := adoptNow
	advance := func(by time.Duration) { mu.Lock(); clock = clock.Add(by); mu.Unlock() }
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }

	stateDir := t.TempDir()
	for id, pane := range map[string]string{"d-slow": "%A", "d-fast": "%B"} {
		rec := liveInteractiveRecord(id)
		rec.Status, rec.PaneID = StatusClosed, pane
		restartRecord(t, stateDir, rec)
	}
	var logs []string
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.now = now
	d.SetStartupLog(func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) })
	d.SetInteractiveRuntime(clockedPresenceRuntime{
		fakeInteractiveRuntime: &fakeInteractiveRuntime{},
		advance:                advance,
		cost:                   map[string]time.Duration{"%A": 3 * time.Second, "%B": time.Second},
	})

	summary := markInterruptedAndReconcile(t, d)

	if summary.Records != 2 {
		t.Fatalf("Records = %d, want 2", summary.Records)
	}
	if len(logs) != 1 {
		t.Fatalf("got %d warnings, want 1: %q", len(logs), logs)
	}
	for _, want := range []string{"slow record d-slow", "took 3s", "slowest: pane presence 3s"} {
		if !strings.Contains(logs[0], want) {
			t.Errorf("warning %q lacks %q", logs[0], want)
		}
	}
}
