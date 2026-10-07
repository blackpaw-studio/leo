package observe

import (
	"slices"
	"testing"
)

func TestAttentionFinishedIsDeferredWhileChildrenAreOutstanding(t *testing.T) {
	tests := []struct {
		name string
		set  func(s *AttentionStore, n int)
	}{
		{"dispatches", func(s *AttentionStore, n int) { s.SetOutstandingDispatches("a", n) }},
		{"subagents", func(s *AttentionStore, n int) { s.SetOutstandingSubagents("a", n) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pub := &recordingPublisher{}
			s := NewAttentionStore(pub)
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
	s := NewAttentionStore(nil)
	s.Set("a", AttentionWorking)
	s.SetOutstandingSubagents("a", 1)

	s.SetOutstandingSubagents("a", 0)

	if got, _ := s.Get("a"); got.State != AttentionWorking {
		t.Fatalf("state = %s; a child ending mid-turn must not finish it", got.State)
	}
}

func TestAttentionNewTurnCancelsDeferredFinish(t *testing.T) {
	s := NewAttentionStore(nil)
	s.Set("a", AttentionWorking)
	s.SetOutstandingDispatches("a", 1)
	s.Set("a", AttentionFinished) // deferred

	s.Set("a", AttentionWorking) // UserPromptSubmit
	s.SetOutstandingDispatches("a", 0)

	if got, _ := s.Get("a"); got.State != AttentionWorking {
		t.Fatalf("state = %s; the new turn is still running", got.State)
	}
}

func TestAttentionAnsweredPromptDuringHoldKeepsDeferredFinish(t *testing.T) {
	s := NewAttentionStore(nil)
	s.Set("a", AttentionWorking)
	s.SetOutstandingSubagents("a", 1)
	s.Set("a", AttentionFinished) // deferred

	s.Set("a", AttentionNeedsInput) // a subagent's permission prompt
	s.Set("a", AttentionWorking)    // answered (PostToolUse)
	s.SetOutstandingSubagents("a", 0)

	if got, _ := s.Get("a"); got.State != AttentionFinished {
		t.Fatalf("state = %s; want the deferred finished", got.State)
	}
}

func TestAttentionLifecycleTransitionDropsDeferredFinish(t *testing.T) {
	s := NewAttentionStore(nil)
	s.Set("a", AttentionWorking)
	s.SetOutstandingSubagents("a", 1)
	s.Set("a", AttentionFinished) // deferred

	s.Set("a", AttentionErrored)
	s.SetOutstandingSubagents("a", 0)

	if got, _ := s.Get("a"); got.State != AttentionErrored {
		t.Fatalf("state = %s; want errored", got.State)
	}
}

func TestAttentionOutstandingChangePublishesWithNewRevision(t *testing.T) {
	pub := &recordingPublisher{}
	s := NewAttentionStore(pub)
	s.Set("a", AttentionWorking)

	s.SetOutstandingDispatches("a", 1)
	s.SetOutstandingDispatches("a", 1) // unchanged: silent

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
	s := NewAttentionStore(pub)

	s.SetOutstandingDispatches("a", 1)
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
	s := NewAttentionStore(nil)
	s.Set("a", AttentionWorking)
	s.SetOutstandingSubagents("a", -3)
	if got, _ := s.Get("a"); got.Outstanding != nil {
		t.Fatalf("outstanding = %+v; want absent", got.Outstanding)
	}
}

func TestAttentionAdvanceSkipsRepeatsAndUntracked(t *testing.T) {
	pub := &recordingPublisher{}
	s := NewAttentionStore(pub)

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
	s := NewAttentionStore(pub)
	s.Set("a", AttentionWorking)
	s.SetOutstandingSubagents("a", 1)
	before := len(pub.events)

	s.Advance("a", AttentionFinished)
	s.Advance("a", AttentionFinished)

	if len(pub.events) != before {
		t.Fatalf("held finish published %d events", len(pub.events)-before)
	}
	s.SetOutstandingSubagents("a", 0)
	if got, _ := s.Get("a"); got.State != AttentionFinished {
		t.Fatalf("state = %s; want finished", got.State)
	}
}

func TestAttentionNeedsInputCarriesClampedReasonUntilCleared(t *testing.T) {
	s := NewAttentionStore(nil)
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
	s := NewAttentionStore(nil)
	s.Set("a", AttentionNeedsInput) // Notification hook, no reason

	att, ok := s.AdvanceNeedsInput("a", AttentionReason{Kind: AttentionReasonQuestion})
	if !ok || att.Reason == nil || att.Reason.Kind != AttentionReasonQuestion {
		t.Fatalf("AdvanceNeedsInput = %+v, %v", att, ok)
	}
}

func TestAttentionMoveCarriesOutstandingAndHold(t *testing.T) {
	s := NewAttentionStore(nil)
	s.Set("old", AttentionWorking)
	s.SetOutstandingSubagents("old", 1)
	s.Set("old", AttentionFinished) // deferred

	s.Move("old", "new")
	s.SetOutstandingSubagents("new", 0)

	if got, _ := s.Get("new"); got.State != AttentionFinished {
		t.Fatalf("state = %s; want the deferred finish under the new name", got.State)
	}
}
