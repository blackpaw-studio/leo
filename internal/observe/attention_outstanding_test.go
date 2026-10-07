package observe

import (
	"slices"
	"testing"
)

// newBridgedStore is a store whose agents "a" and "b" launched under the
// bridge keys "a-key" and "b-key", as the supervisor binds them.
func newBridgedStore(pub Publisher) *AttentionStore {
	s := NewAttentionStore(pub)
	s.BindBridgeKey("a-key", "a")
	s.BindBridgeKey("b-key", "b")
	return s
}

// setDispatches sets key's outstanding dispatches directly, as a newer
// dispatch snapshot would.
func setDispatches(s *AttentionStore, key string, n int) {
	s.setOutstanding(key, func(o *Outstanding) { o.Dispatches = max(n, 0) })
}

func TestAttentionFinishedIsDeferredWhileChildrenAreOutstanding(t *testing.T) {
	tests := []struct {
		name string
		set  func(s *AttentionStore, n int)
	}{
		{"dispatches", func(s *AttentionStore, n int) { setDispatches(s, "a-key", n) }},
		{"subagents", func(s *AttentionStore, n int) { s.SetOutstandingSubagents("a-key", n) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pub := &recordingPublisher{}
			s := newBridgedStore(pub)
			s.Set("a", AttentionWorking)
			tt.set(s, 2)

			s.Set("a", AttentionFinished)
			if got, _ := s.Get("a"); got.State != AttentionWorking {
				t.Fatalf("after Stop with children: %s; want working", got.State)
			}
			tt.set(s, 1)
			if got, _ := s.Get("a"); got.State != AttentionWorking {
				t.Fatalf("one child left: %s; want working", got.State)
			}
			tt.set(s, 0)

			got, _ := s.Get("a")
			if got.State != AttentionFinished || got.Outstanding != nil {
				t.Fatalf("last child ended: %+v; want finished, no outstanding", got)
			}
			if trace := attentionTrace(pub.events, "a"); !slices.Equal(trace, []AttentionState{AttentionWorking, AttentionFinished}) {
				t.Fatalf("trace = %v", trace)
			}
		})
	}
}

func TestAttentionChildEndingWithoutDeferredFinishStaysWorking(t *testing.T) {
	s := newBridgedStore(nil)
	s.Set("a", AttentionWorking)
	s.SetOutstandingSubagents("a-key", 1)

	s.SetOutstandingSubagents("a-key", 0)

	if got, _ := s.Get("a"); got.State != AttentionWorking {
		t.Fatalf("state = %s; a child ending mid-turn must not finish it", got.State)
	}
}

func TestAttentionNewTurnCancelsDeferredFinish(t *testing.T) {
	s := newBridgedStore(nil)
	s.Set("a", AttentionWorking)
	setDispatches(s, "a-key", 1)
	s.Set("a", AttentionFinished) // deferred

	s.Set("a", AttentionWorking) // UserPromptSubmit
	setDispatches(s, "a-key", 0)

	if got, _ := s.Get("a"); got.State != AttentionWorking {
		t.Fatalf("state = %s; the new turn is still running", got.State)
	}
}

func TestAttentionAnsweredPromptDuringHoldKeepsDeferredFinish(t *testing.T) {
	s := newBridgedStore(nil)
	s.Set("a", AttentionWorking)
	s.SetOutstandingSubagents("a-key", 1)
	s.Set("a", AttentionFinished) // deferred

	s.Set("a", AttentionNeedsInput) // a subagent's permission prompt
	s.Set("a", AttentionWorking)    // answered (PostToolUse)
	s.SetOutstandingSubagents("a-key", 0)

	if got, _ := s.Get("a"); got.State != AttentionFinished {
		t.Fatalf("state = %s; want the deferred finished", got.State)
	}
}

func TestAttentionLifecycleTransitionDropsDeferredFinish(t *testing.T) {
	s := newBridgedStore(nil)
	s.Set("a", AttentionWorking)
	s.SetOutstandingSubagents("a-key", 1)
	s.Set("a", AttentionFinished) // deferred

	s.Set("a", AttentionErrored)
	s.SetOutstandingSubagents("a-key", 0)

	if got, _ := s.Get("a"); got.State != AttentionErrored {
		t.Fatalf("state = %s; want errored", got.State)
	}
}

func TestAttentionOutstandingChangePublishesWithNewRevision(t *testing.T) {
	pub := &recordingPublisher{}
	s := newBridgedStore(pub)
	s.Set("a", AttentionWorking)

	setDispatches(s, "a-key", 1)
	setDispatches(s, "a-key", 1) // unchanged: silent

	if len(pub.events) != 2 {
		t.Fatalf("published %d events; want 2", len(pub.events))
	}
	p := pub.events[1].Payload.(*AgentActivityPayload)
	if p.Attention.Revision != 2 || p.Attention.Outstanding == nil || p.Attention.Outstanding.Dispatches != 1 {
		t.Fatalf("attention = %+v", p.Attention)
	}
}

func TestAttentionOutstandingForUntrackedAgentIsKeptSilently(t *testing.T) {
	pub := &recordingPublisher{}
	s := newBridgedStore(pub)

	setDispatches(s, "a-key", 1)
	if len(pub.events) != 0 {
		t.Fatalf("published for an untracked agent: %v", eventTypes(pub.events))
	}
	if _, ok := s.Get("a"); ok {
		t.Fatal("outstanding invented an attention source")
	}

	s.Set("a", AttentionWorking)
	got, _ := s.Get("a")
	if got.Outstanding == nil || got.Outstanding.Dispatches != 1 {
		t.Fatalf("outstanding = %+v; want the earlier count", got.Outstanding)
	}
}

func TestAttentionNegativeOutstandingClampsToZero(t *testing.T) {
	s := newBridgedStore(nil)
	s.Set("a", AttentionWorking)
	s.SetOutstandingSubagents("a-key", -3)
	if got, _ := s.Get("a"); got.Outstanding != nil {
		t.Fatalf("outstanding = %+v; want absent", got.Outstanding)
	}
}

func TestAttentionAdvanceSkipsRepeatsAndUntracked(t *testing.T) {
	pub := &recordingPublisher{}
	s := newBridgedStore(pub)

	if _, ok := s.Advance("ghost", AttentionWorking); ok {
		t.Fatal("Advance invented an attention source")
	}
	s.Set("a", AttentionWorking)
	if _, ok := s.Advance("a", AttentionWorking); ok {
		t.Fatal("Advance to the current state must be a no-op")
	}
	if _, ok := s.Advance("a", AttentionWorking, AttentionNeedsInput); ok {
		t.Fatal("Advance with an unmet from must be a no-op")
	}
	if _, ok := s.Advance("a", AttentionFinished); !ok {
		t.Fatal("Advance to a new state must apply")
	}
	if _, ok := s.Advance("a", AttentionFinished); ok {
		t.Fatal("a second finished must be a no-op")
	}
	if len(pub.events) != 2 {
		t.Fatalf("published %d events; want 2", len(pub.events))
	}
}

func TestAttentionAdvanceFinishedDuringHoldIsSilent(t *testing.T) {
	pub := &recordingPublisher{}
	s := newBridgedStore(pub)
	s.Set("a", AttentionWorking)
	s.SetOutstandingSubagents("a-key", 1)
	before := len(pub.events)

	s.Advance("a", AttentionFinished)
	s.Advance("a", AttentionFinished)

	if len(pub.events) != before {
		t.Fatalf("held finish published %d events", len(pub.events)-before)
	}
	s.SetOutstandingSubagents("a-key", 0)
	if got, _ := s.Get("a"); got.State != AttentionFinished {
		t.Fatalf("state = %s; want finished", got.State)
	}
}

func TestAttentionNeedsInputCarriesClampedReasonUntilCleared(t *testing.T) {
	s := newBridgedStore(nil)
	s.Set("a", AttentionWorking)

	att, ok := s.AdvanceNeedsInput("a", AttentionReason{Kind: AttentionReasonPermission, Tool: "Bash", Detail: "rm\n-rf\x1b[31m /tmp/x"})
	if !ok || att.Reason == nil || att.Reason.Detail != "rm -rf /tmp/x" {
		t.Fatalf("AdvanceNeedsInput = %+v, %v", att.Reason, ok)
	}
	// The same reason again (hook and mod both report it) is a no-op.
	if _, ok := s.AdvanceNeedsInput("a", AttentionReason{Kind: AttentionReasonPermission, Tool: "Bash", Detail: "rm -rf /tmp/x"}); ok {
		t.Fatal("repeated reason must be a no-op")
	}

	s.Advance("a", AttentionWorking, AttentionNeedsInput)
	if got, _ := s.Get("a"); got.Reason != nil {
		t.Fatalf("reason = %+v after clearing", got.Reason)
	}
}

func TestAttentionHookNeedsInputGainsReasonFromBridge(t *testing.T) {
	s := newBridgedStore(nil)
	s.Set("a", AttentionNeedsInput) // Notification hook, no reason

	att, ok := s.AdvanceNeedsInput("a", AttentionReason{Kind: AttentionReasonQuestion})
	if !ok || att.Reason == nil || att.Reason.Kind != AttentionReasonQuestion {
		t.Fatalf("AdvanceNeedsInput = %+v, %v", att, ok)
	}
}

func TestAttentionMoveCarriesOutstandingAndHold(t *testing.T) {
	s := NewAttentionStore(nil)
	s.BindBridgeKey("k", "old")
	s.Set("old", AttentionWorking)
	s.SetOutstandingSubagents("k", 1)
	s.Set("old", AttentionFinished) // deferred

	s.Move("old", "new")
	if got, _ := s.Get("new"); got.State != AttentionWorking || got.Outstanding == nil || got.Outstanding.Subagents != 1 {
		t.Fatalf("after the rename: %+v; want the hold and its count carried", got)
	}
	s.SetOutstandingSubagents("k", 0)

	if got, _ := s.Get("new"); got.State != AttentionFinished {
		t.Fatalf("state = %s; want the deferred finish under the new name", got.State)
	}
}

// staticCounter is a DispatchSnapshotter returning a settable map, each
// read under the next generation.
type staticCounter struct {
	gen    uint64
	counts map[string]int
}

func (c *staticCounter) DispatchSnapshot() (uint64, map[string]int) {
	c.gen++
	return c.gen, c.counts
}

// take is a snapshot of counts as a tick would read it now.
func (c *staticCounter) take(counts map[string]int) (uint64, map[string]int) {
	c.counts = counts
	return c.DispatchSnapshot()
}

// A dispatch started just before the turn ends is counted when the finish
// lands, not a tick later.
func TestAttentionFinishReadsDispatchCountsSynchronously(t *testing.T) {
	counter := &staticCounter{counts: map[string]int{"a-key": 1}}
	s := newBridgedStore(nil)
	s.SetDispatchCounter(counter)
	s.Set("a", AttentionWorking)

	s.Set("a", AttentionFinished)
	if got, _ := s.Get("a"); got.State != AttentionWorking || got.Outstanding == nil || got.Outstanding.Dispatches != 1 {
		t.Fatalf("after finish with a fresh dispatch: %+v; want held", got)
	}

	// It ended before any tick saw it: the next reconcile releases it.
	s.ReconcileDispatches(counter.take(map[string]int{}))
	if got, _ := s.Get("a"); got.State != AttentionFinished {
		t.Fatalf("after reconcile: %+v; want finished", got)
	}
}

func TestAttentionFinishViaAdvanceAndTokenReadDispatchCounts(t *testing.T) {
	counter := &staticCounter{counts: map[string]int{"a-key": 1}}
	s := newBridgedStore(nil)
	s.SetDispatchCounter(counter)
	s.RegisterToken("tok", "a")
	s.Set("a", AttentionWorking)

	s.Advance("a", AttentionFinished)
	s.SetByToken("tok", AttentionFinished)

	if got, _ := s.Get("a"); got.State != AttentionWorking {
		t.Fatalf("state = %s; want held", got.State)
	}
}

// R1: a tick reads its snapshot (no dispatch yet) and is preempted; a
// dispatch starts and the turn completes, reading a newer snapshot and
// holding. The tick's stale zero must not release the hold.
func TestAttentionRejectsAStaleDispatchSnapshot(t *testing.T) {
	counter := &staticCounter{}
	s := newBridgedStore(nil)
	s.SetDispatchCounter(counter)
	s.Set("a", AttentionWorking)

	staleGen, staleCounts := counter.take(map[string]int{})
	counter.counts = map[string]int{"a-key": 1}
	s.Set("a", AttentionFinished)
	s.ReconcileDispatches(staleGen, staleCounts)

	if got, _ := s.Get("a"); got.State != AttentionWorking || got.Outstanding == nil || got.Outstanding.Dispatches != 1 {
		t.Fatalf("after the stale reconcile: %+v; want still held", got)
	}
	s.ReconcileDispatches(counter.take(map[string]int{"a-key": 1}))
	if got, _ := s.Get("a"); got.State != AttentionWorking {
		t.Fatalf("a current snapshot still counting it: %+v; want held", got)
	}
	s.ReconcileDispatches(counter.take(map[string]int{}))
	if got, _ := s.Get("a"); got.State != AttentionFinished {
		t.Fatalf("after the dispatch ended: %+v; want finished", got)
	}
}

// The mirror race: a completion reads its snapshot, a tick reads a newer
// one after the dispatch ended and applies first; the completion's older
// count must not re-hold the agent.
func TestAttentionCompletionWithAStaleSnapshotDoesNotRehold(t *testing.T) {
	s := newBridgedStore(nil)
	s.Set("a", AttentionWorking)
	s.ReconcileDispatches(2, map[string]int{})

	s.SetDispatchCounter(fixedSnapshot{gen: 1, counts: map[string]int{"a-key": 1}})
	s.Set("a", AttentionFinished)

	if got, _ := s.Get("a"); got.State != AttentionFinished {
		t.Fatalf("state = %+v; the newer snapshot said nothing is outstanding", got)
	}
}

type fixedSnapshot struct {
	gen    uint64
	counts map[string]int
}

func (f fixedSnapshot) DispatchSnapshot() (uint64, map[string]int) { return f.gen, f.counts }

// hookedSnapshot is a DispatchSnapshotter whose first read runs during
// before returning, so another completion interleaves deterministically.
type hookedSnapshot struct {
	gen    uint64
	counts map[string]int
	during func()
}

func (h *hookedSnapshot) DispatchSnapshot() (uint64, map[string]int) {
	h.gen++
	gen := h.gen
	if f := h.during; f != nil {
		h.during = nil
		f()
	}
	return gen, h.counts
}

// Concurrent completions for A and B: A reads gen 1, B reads gen 2 and
// applies first, then A's gen 1 is rejected. Both snapshots count A's
// dispatch, so A must still be held, not finished on a stale zero.
func TestAttentionConcurrentCompletionsHoldEachAgentByTheNewestSnapshot(t *testing.T) {
	s := newBridgedStore(nil)
	s.Set("a", AttentionWorking)
	s.Set("b", AttentionWorking)
	counter := &hookedSnapshot{counts: map[string]int{"a-key": 1}}
	counter.during = func() { s.Set("b", AttentionFinished) }
	s.SetDispatchCounter(counter)

	s.Set("a", AttentionFinished)

	if got, _ := s.Get("a"); got.State != AttentionWorking || got.Outstanding == nil || got.Outstanding.Dispatches != 1 {
		t.Fatalf("a = %+v; want held by its dispatch", got)
	}
	if got, _ := s.Get("b"); got.State != AttentionFinished {
		t.Fatalf("b = %+v; want finished", got)
	}
	if counter.gen != 2 {
		t.Fatalf("snapshot reads = %d; want the interleaving to have run", counter.gen)
	}
}

// A token transition skipped by its from filter still applies its
// snapshot in full: a held agent whose last dispatch ended is released,
// not left with a silently zeroed count no later tick would change.
func TestAttentionSkippedTokenTransitionStillReleasesAHold(t *testing.T) {
	counter := &staticCounter{counts: map[string]int{"a-key": 1}}
	s := newBridgedStore(nil)
	s.SetDispatchCounter(counter)
	s.RegisterToken("tok", "a")
	s.Set("a", AttentionWorking)
	s.Set("a", AttentionFinished) // held

	counter.counts = map[string]int{}
	if _, ok := s.SetByToken("tok", AttentionFinished, AttentionNeedsInput); ok {
		t.Fatal("transition applied despite its from filter")
	}

	if got, _ := s.Get("a"); got.State != AttentionFinished {
		t.Fatalf("a = %+v; want released to finished", got)
	}
}

// Two agents swap names (through a temporary one) while each is held:
// each hold stays with the agent whose key's children are running.
func TestAttentionNameSwapKeepsEachHoldWithItsKey(t *testing.T) {
	s := newBridgedStore(nil)
	for _, agent := range []string{"a", "b"} {
		s.Set(agent, AttentionWorking)
	}
	s.SetOutstandingSubagents("a-key", 1)
	setDispatches(s, "b-key", 1)
	s.Set("a", AttentionFinished)
	s.Set("b", AttentionFinished)

	s.Move("a", "tmp")
	s.Move("b", "a")
	s.Move("tmp", "b")
	s.SetOutstandingSubagents("a-key", 0) // now agent "b"'s child

	if got, _ := s.Get("b"); got.State != AttentionFinished {
		t.Fatalf("b (a-key) = %+v; want released", got)
	}
	if got, _ := s.Get("a"); got.State != AttentionWorking || got.Outstanding == nil || got.Outstanding.Dispatches != 1 {
		t.Fatalf("a (b-key) = %+v; want still held by its dispatch", got)
	}
}

// A name deleted and recreated under a new launch key does not inherit
// the old launch's children.
func TestAttentionRecreatedNameDoesNotInheritAnotherKeysCount(t *testing.T) {
	s := NewAttentionStore(nil)
	s.BindBridgeKey("k1", "a")
	s.SetOutstandingSubagents("k1", 2)
	s.UnregisterAgent("a")
	s.Remove("a")

	s.BindBridgeKey("k2", "a")
	s.Set("a", AttentionWorking)
	s.Set("a", AttentionFinished)

	if got, _ := s.Get("a"); got.State != AttentionFinished || got.Outstanding != nil {
		t.Fatalf("recreated a = %+v; want finished with nothing outstanding", got)
	}
}

// A launch bound after the supervisor's rename but before the store's Move
// keeps its binding: the moved one is the older launch's.
func TestAttentionBindingMadeBeforeMoveWins(t *testing.T) {
	s := NewAttentionStore(nil)
	s.BindBridgeKey("k-old", "old")
	s.BindBridgeKey("k-new", "new")
	s.Set("old", AttentionWorking)
	s.SetOutstandingSubagents("k-new", 1)

	s.Move("old", "new")

	if got, _ := s.Get("new"); got.Outstanding == nil || got.Outstanding.Subagents != 1 {
		t.Fatalf("new = %+v; want the later launch's count", got)
	}
}

// Rebinding a launch's key (a relaunch) moves its count to the agent and
// releases a hold the old launch's children no longer justify.
func TestAttentionRebindToAnIdleKeyReleasesTheHold(t *testing.T) {
	s := newBridgedStore(nil)
	s.Set("a", AttentionWorking)
	s.SetOutstandingSubagents("a-key", 1)
	s.Set("a", AttentionFinished) // held

	s.BindBridgeKey("a-key2", "a")

	if got, _ := s.Get("a"); got.State != AttentionFinished || got.Outstanding != nil {
		t.Fatalf("a = %+v; want released by the new launch", got)
	}
}
