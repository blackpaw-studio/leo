package observe

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

// staticActivity is an ActivityProvider returning a fixed map.
type staticActivity map[string]AgentActivity

func (s staticActivity) Activities() map[string]AgentActivity { return s }

func TestAttentionSetBumpsRevisionEveryTimeIncludingSameState(t *testing.T) {
	s := NewAttentionStore(nil)

	first := s.Set("a", AttentionFinished)
	second := s.Set("a", AttentionFinished)

	if first.Revision != 1 || second.Revision != 2 {
		t.Fatalf("revisions = %d, %d; want 1, 2", first.Revision, second.Revision)
	}
	got, ok := s.Get("a")
	if !ok || got != (AgentAttention{State: AttentionFinished, Revision: 2}) {
		t.Fatalf("Get = %+v, %v", got, ok)
	}
}

func TestAttentionRevisionsAreIsolatedPerAgent(t *testing.T) {
	s := NewAttentionStore(nil)

	s.Set("a", AttentionWorking)
	s.Set("a", AttentionFinished)
	b := s.Set("b", AttentionWorking)

	if b.Revision != 1 {
		t.Fatalf("b revision = %d, want 1", b.Revision)
	}
}

func TestAttentionRemoveThenSetContinuesRevision(t *testing.T) {
	s := NewAttentionStore(nil)
	s.Set("a", AttentionWorking)
	s.Set("a", AttentionFinished)

	s.Remove("a")
	if _, ok := s.Get("a"); ok {
		t.Fatal("Get after Remove reported present")
	}
	again := s.Set("a", AttentionWorking)

	if again.Revision != 3 {
		t.Fatalf("revision after Remove+Set = %d, want 3", again.Revision)
	}
}

func TestAttentionMoveBumpsPastBothNames(t *testing.T) {
	pub := &recordingPublisher{}
	s := NewAttentionStore(pub)
	for range 3 {
		s.Set("new", AttentionFinished)
	}
	s.Remove("new") // a deleted agent's revision (3) outlives it
	s.Set("old", AttentionNeedsInput)
	pub.events = nil

	s.Move("old", "new")

	if _, ok := s.Get("old"); ok {
		t.Fatal("old name still present after Move")
	}
	got, ok := s.Get("new")
	if !ok || got != (AgentAttention{State: AttentionNeedsInput, Revision: 4}) {
		t.Fatalf("Get(new) = %+v, %v; want needs_input rev 4 (max(1,3)+1)", got, ok)
	}
	if len(pub.events) != 1 {
		t.Fatalf("Move published %d events, want 1", len(pub.events))
	}
	p := pub.events[0].Payload.(*AgentActivityPayload)
	if p.Agent != "new" || p.Attention == nil || *p.Attention != got {
		t.Fatalf("Move payload = %+v", p)
	}
	if next := s.Set("new", AttentionWorking); next.Revision != 5 {
		t.Fatalf("revision after Move+Set = %d, want 5", next.Revision)
	}
}

func TestAttentionAllReturnsCopy(t *testing.T) {
	s := NewAttentionStore(nil)
	s.Set("a", AttentionWorking)

	all := s.All()
	all["a"] = AgentAttention{State: AttentionErrored, Revision: 99}
	delete(all, "a")

	if got, _ := s.Get("a"); got.State != AttentionWorking {
		t.Fatalf("mutating All() leaked into store: %+v", got)
	}
}

func TestAttentionSetPublishesActivityWithAttention(t *testing.T) {
	pub := &recordingPublisher{}
	s := NewAttentionStore(pub)
	s.SetActivityProvider(staticActivity{"a": {Activity: ActivityIdle, CurrentAction: &Action{Kind: ActionKindPane, Detail: "x"}}})

	s.Set("a", AttentionNeedsInput)

	if len(pub.events) != 1 || pub.events[0].Type != EventAgentActivity {
		t.Fatalf("events = %+v", pub.events)
	}
	p := pub.events[0].Payload.(*AgentActivityPayload)
	if p.Agent != "a" || p.Activity != ActivityIdle || p.CurrentAction == nil || p.CurrentAction.Detail != "x" {
		t.Fatalf("payload activity = %+v", p)
	}
	if p.Attention == nil || *p.Attention != (AgentAttention{State: AttentionNeedsInput, Revision: 1}) {
		t.Fatalf("payload attention = %+v", p.Attention)
	}
}

func TestAttentionSetWithoutActivityReadingReportsUnknownActivity(t *testing.T) {
	pub := &recordingPublisher{}
	s := NewAttentionStore(pub)

	s.Set("a", AttentionWorking)

	p := pub.events[0].Payload.(*AgentActivityPayload)
	if p.Activity != ActivityUnknown {
		t.Fatalf("activity = %q, want unknown", p.Activity)
	}
}

func TestAttentionNilStoreIsSafe(t *testing.T) {
	var s *AttentionStore
	s.Set("a", AttentionWorking)
	s.Remove("a")
	s.Move("a", "b")
	if _, ok := s.Get("a"); ok {
		t.Fatal("nil store reported attention")
	}
	if len(s.All()) != 0 {
		t.Fatal("nil store All non-empty")
	}
}

func TestActivityPayloadOmitsAbsentAttention(t *testing.T) {
	b, err := json.Marshal(&AgentActivityPayload{Agent: "a", Activity: ActivityIdle})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "attention") {
		t.Fatalf("payload = %s, want no attention key", b)
	}
	b, _ = json.Marshal(&AgentActivityPayload{Agent: "a", Attention: &AgentAttention{State: AttentionUnknown, Revision: 3}})
	if !strings.Contains(string(b), `"attention":{"state":"unknown","revision":3}`) {
		t.Fatalf("payload = %s, want attention object", b)
	}
}

func TestTrackerSweepCopiesAttentionWithoutBumping(t *testing.T) {
	restore := listSessionActivityFn
	t.Cleanup(func() { listSessionActivityFn = restore })
	now := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	listSessionActivityFn = fakeActivity(map[string]time.Time{"leo-a": now.Add(-time.Minute)})

	pub := &recordingPublisher{}
	store := NewAttentionStore(nil)
	store.Set("a", AttentionFinished)
	tr := NewTracker("", func() map[string]string { return map[string]string{"a": "leo-a"} }, pub,
		WithClock(func() time.Time { return now }), WithAttention(store))

	tr.sweep(t.Context())

	if len(pub.events) != 1 {
		t.Fatalf("events = %d, want 1", len(pub.events))
	}
	p := pub.events[0].Payload.(*AgentActivityPayload)
	if p.Attention == nil || *p.Attention != (AgentAttention{State: AttentionFinished, Revision: 1}) {
		t.Fatalf("sweep attention = %+v", p.Attention)
	}
	if got, _ := store.Get("a"); got.Revision != 1 {
		t.Fatalf("sweep bumped revision to %d", got.Revision)
	}
}

// fakeActivity fakes tmux.ListSessionActivity with fixed last-activity times.
func fakeActivity(last map[string]time.Time) func(context.Context, string) (map[string]tmux.SessionActivity, error) {
	return func(context.Context, string) (map[string]tmux.SessionActivity, error) {
		out := make(map[string]tmux.SessionActivity, len(last))
		for name, at := range last {
			out[name] = tmux.SessionActivity{LastActivity: at}
		}
		return out, nil
	}
}

func TestAttentionSetIfTrackedOnlyTransitionsTrackedAgents(t *testing.T) {
	s := NewAttentionStore(nil)
	s.Set("tracked", AttentionWorking)

	got, ok := s.SetIfTracked("tracked", AttentionErrored)
	_, untrackedOK := s.SetIfTracked("untracked", AttentionErrored)

	if !ok || got != (AgentAttention{State: AttentionErrored, Revision: 2}) {
		t.Fatalf("tracked = %+v, %v", got, ok)
	}
	if untrackedOK {
		t.Fatal("untracked agent transitioned")
	}
	if _, present := s.Get("untracked"); present {
		t.Fatal("SetIfTracked created an entry")
	}
}

func TestAttentionTokenRoutesToCurrentName(t *testing.T) {
	s := NewAttentionStore(nil)
	s.RegisterToken("tok-a", "old")

	s.Move("old", "new")
	att, ok := s.SetByToken("tok-a", AttentionWorking)

	if !ok || att.State != AttentionWorking {
		t.Fatalf("SetByToken after rename = %+v, %v", att, ok)
	}
	if got, ok := s.Get("new"); !ok || got != att {
		t.Fatalf("Get(new) = %+v, %v; want %+v", got, ok, att)
	}
	if _, ok := s.Get("old"); ok {
		t.Fatal("hook landed on the old name")
	}
	if name, ok := s.AgentForToken("tok-a"); !ok || name != "new" {
		t.Fatalf("AgentForToken = %q, %v", name, ok)
	}
}

func TestAttentionUnknownOrUnregisteredTokenIsNoop(t *testing.T) {
	s := NewAttentionStore(nil)
	s.RegisterToken("tok-a", "a")
	s.RegisterToken("tok-b", "b")
	s.Set("b", AttentionFinished)

	s.UnregisterAgent("a")
	s.Remove("b")

	for _, tok := range []string{"tok-a", "tok-b", "never", ""} {
		if att, ok := s.SetByToken(tok, AttentionWorking); ok {
			t.Errorf("SetByToken(%q) = %+v, want no-op", tok, att)
		}
	}
	if len(s.All()) != 0 {
		t.Fatalf("All = %+v, want empty", s.All())
	}
}

func TestAttentionUnregisterTokenLeavesOtherGenerations(t *testing.T) {
	s := NewAttentionStore(nil)
	s.RegisterToken("gen1", "a")
	s.RegisterToken("gen2", "a")

	s.UnregisterToken("gen1")

	if _, ok := s.SetByToken("gen1", AttentionWorking); ok {
		t.Error("stale generation token still routes")
	}
	if _, ok := s.SetByToken("gen2", AttentionWorking); !ok {
		t.Error("live generation token stopped routing")
	}
}

func TestAttentionSetByTokenFromGuard(t *testing.T) {
	s := NewAttentionStore(nil)
	s.RegisterToken("tok", "a")
	s.Set("a", AttentionWorking)

	if att, ok := s.SetByToken("tok", AttentionWorking, AttentionNeedsInput); ok {
		t.Fatalf("guarded transition from working applied: %+v", att)
	}
	if got, _ := s.Get("a"); got.Revision != 1 {
		t.Fatalf("guarded no-op churned revision: %+v", got)
	}
	s.Set("a", AttentionNeedsInput)
	if att, ok := s.SetByToken("tok", AttentionWorking, AttentionNeedsInput); !ok || att != (AgentAttention{State: AttentionWorking, Revision: 3}) {
		t.Fatalf("guarded transition from needs_input = %+v, %v", att, ok)
	}
}
