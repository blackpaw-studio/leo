package observe

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

func tasksPending(counts map[string]int) *bridge.PendingWork {
	return &bridge.PendingWork{Tasks: counts}
}

func (h *feedHarness) attentionNow(t *testing.T) AgentAttention {
	t.Helper()
	att, ok := h.store.Get("alice")
	if !ok {
		t.Fatal("alice has no attention")
	}
	return att
}

// completeWithTasks runs one turn that ends with n background tasks live.
func (h *feedHarness) completeWithTasks(id string, counts map[string]int) {
	h.send(bridge.Event{Name: bridge.EventTurnStart, EventID: "ts-" + id})
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "tc-" + id, Pending: tasksPending(counts)})
}

func TestBridgeFeedBackgroundTasksHoldWorking(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.completeWithTasks("1", map[string]int{"bash": 1, "monitor": 1})

	att := h.attentionNow(t)
	if att.State != AttentionWorking || att.Outstanding == nil || att.Outstanding.Tasks != 2 {
		t.Fatalf("attention = %+v; want working held on 2 tasks", att)
	}
}

func TestBridgeFeedTasksFieldIsAdditiveOnTheWire(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.EventTurnStart, EventID: "ts1"})
	h.send(bridge.Event{Name: bridge.EventSubagents, Subagents: &bridge.SubagentsReport{Running: 1}})
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "tc1"})
	raw, err := json.Marshal(h.attentionNow(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "tasks") || !strings.Contains(string(raw), `"outstanding":{"dispatches":0,"subagents":1}`) {
		t.Fatalf("wire = %s; tasks must be absent at zero", raw)
	}

	h.completeWithTasks("2", map[string]int{"bash": 3})
	raw, _ = json.Marshal(h.attentionNow(t).Outstanding)
	if string(raw) != `{"dispatches":0,"subagents":1,"tasks":3}` {
		t.Fatalf("outstanding = %s", raw)
	}
}

func TestBridgeFeedWakeupsNeverHold(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.EventTurnStart, EventID: "ts1"})
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "tc1", Pending: &bridge.PendingWork{Wakeups: 3}})

	if att := h.attentionNow(t); att.State != AttentionFinished || att.Outstanding != nil {
		t.Fatalf("attention = %+v; a timer wait is idle", att)
	}
}

func TestBridgeFeedZeroCountTasksDoNotHold(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.completeWithTasks("1", map[string]int{"bash": 0})

	if att := h.attentionNow(t); att.State != AttentionFinished {
		t.Fatalf("attention = %+v", att)
	}
}

func TestBridgeFeedNextTurnStartDropsTheHoldAndItsCompletionDecidesAgain(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.completeWithTasks("1", map[string]int{"bash": 1})
	rev := h.attentionNow(t).Revision

	h.send(bridge.Event{Name: bridge.EventTurnStart, EventID: "ts2"})
	att := h.attentionNow(t)
	if att.State != AttentionWorking || att.Outstanding != nil || att.Revision <= rev {
		t.Fatalf("after turn.start = %+v; want working, hold gone, new revision", att)
	}
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "tc2"})
	if att := h.attentionNow(t); att.State != AttentionFinished || att.Outstanding != nil {
		t.Fatalf("after clean complete = %+v; pending never carries over", att)
	}

	h.completeWithTasks("3", map[string]int{"bash": 1})
	if att := h.attentionNow(t); att.State != AttentionWorking || att.Outstanding == nil || att.Outstanding.Tasks != 1 {
		t.Fatalf("after complete with a task = %+v; want held again", att)
	}
}

func TestBridgeFeedPendingIsReadPerCompletion(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.completeWithTasks("1", map[string]int{"bash": 2})
	// A later complete with fewer tasks replaces the count, not adds to it.
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "tc2", Pending: tasksPending(map[string]int{"bash": 1})})

	if att := h.attentionNow(t); att.State != AttentionWorking || att.Outstanding == nil || att.Outstanding.Tasks != 1 {
		t.Fatalf("attention = %+v; want 1 task", att)
	}
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "tc3"})
	if att := h.attentionNow(t); att.State != AttentionFinished || att.Outstanding != nil {
		t.Fatalf("attention = %+v; a complete with no pending releases", att)
	}
}

// A dev server or watcher never wakes the session: nothing but the next
// turn, an interrupt, a stop or the session ending lets go of the hold.
func TestBridgeFeedNeverEndingTaskReleasesOnlyOnTheDocumentedEvents(t *testing.T) {
	quiet := []bridge.Event{
		{Name: bridge.EventActivity, Activity: &bridge.ActivityReport{Tool: "Bash"}},
		{Name: bridge.EventSubagents, Subagents: &bridge.SubagentsReport{Running: 0}},
		{Name: bridge.ReportHello},
		{Name: bridge.EventCompact, Compact: &bridge.CompactReport{Phase: bridge.CompactStarted, Trigger: bridge.CompactAuto}},
	}
	releases := map[string]struct {
		do   func(h *feedHarness)
		want AttentionState
	}{
		"turn.start": {func(h *feedHarness) { h.send(bridge.Event{Name: bridge.EventTurnStart, EventID: "ts2"}) }, AttentionWorking},
		"interrupt": {func(h *feedHarness) {
			h.send(bridge.Event{Name: bridge.EventTurnStart, EventID: "ts2"})
			h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "tc2", Reason: "aborted", Pending: tasksPending(map[string]int{"bash": 1})})
		}, AttentionFinished},
		"session.end": {func(h *feedHarness) { h.send(bridge.Event{Name: bridge.EventSessionEnd, EventID: "se1"}) }, AttentionFinished},
		"stop": {func(h *feedHarness) {
			h.store.UnbindBridgeLaunch("alice-key", "alice-key-launch")
		}, AttentionFinished},
	}
	for name, tc := range releases {
		t.Run(name, func(t *testing.T) {
			h := newFeedHarness(t, nil)
			h.completeWithTasks("1", map[string]int{"bash": 1})
			for _, ev := range quiet {
				h.send(ev)
				if att := h.attentionNow(t); att.State != AttentionWorking || att.Outstanding == nil || att.Outstanding.Tasks != 1 {
					t.Fatalf("after %s the hold = %+v; only a release event may drop it", ev.Name, att)
				}
			}

			tc.do(h)

			att := h.attentionNow(t)
			if att.State != tc.want || att.Outstanding != nil {
				t.Fatalf("after %s attention = %+v; want %s with no hold", name, att, tc.want)
			}
		})
	}
}

func TestBridgeFeedTasksHoldAlongsideSubagentsUntilBothEnd(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.EventTurnStart, EventID: "ts1"})
	h.send(bridge.Event{Name: bridge.EventSubagents, Subagents: &bridge.SubagentsReport{Running: 1}})
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "tc1", Pending: tasksPending(map[string]int{"bash": 1})})

	h.send(bridge.Event{Name: bridge.EventSubagents, Subagents: &bridge.SubagentsReport{Running: 0}})

	att := h.attentionNow(t)
	if att.State != AttentionWorking || att.Outstanding == nil || att.Outstanding.Tasks != 1 || att.Outstanding.Subagents != 0 {
		t.Fatalf("attention = %+v; the task still holds", att)
	}
}

func TestBridgeFeedAnsweringAPromptDuringATaskHoldKeepsIt(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.completeWithTasks("1", map[string]int{"bash": 1})
	h.send(bridge.Event{Name: bridge.EventAttention, Attention: &bridge.AttentionReport{State: bridge.AttentionNeedsInput, Kind: bridge.AttentionPermission, Tool: "Bash"}})
	h.send(bridge.Event{Name: bridge.EventAttention, Attention: &bridge.AttentionReport{State: bridge.AttentionCleared}})

	if att := h.attentionNow(t); att.State != AttentionWorking || att.Outstanding == nil || att.Outstanding.Tasks != 1 {
		t.Fatalf("attention = %+v; the hold survives an answered prompt", att)
	}
}

func TestBridgeFeedTaskHoldNeverOutlivesALaunchChange(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.completeWithTasks("1", map[string]int{"bash": 1})

	// The agent restarts: the new launch binds to the same key and name.
	h.store.BindBridgeKey("alice-key", "alice-key-launch2", "alice")

	if att := h.attentionNow(t); att.State != AttentionFinished || att.Outstanding != nil {
		t.Fatalf("attention = %+v; the old launch's shell died with it", att)
	}
	// A straggler from the old launch cannot restore it.
	h.send(bridge.Event{Name: bridge.EventTurnStart, EventID: "late-ts"})
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "late-tc", Pending: tasksPending(map[string]int{"bash": 1})})
	if att := h.attentionNow(t); att.Outstanding != nil {
		t.Fatalf("attention = %+v; a retired launch's report must not hold", att)
	}
}

func TestBridgeFeedTaskHoldFollowsARename(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.completeWithTasks("1", map[string]int{"bash": 1})

	h.store.Move("alice", "bob")

	att, _ := h.store.Get("bob")
	if att.State != AttentionWorking || att.Outstanding == nil || att.Outstanding.Tasks != 1 {
		t.Fatalf("bob = %+v", att)
	}
}
