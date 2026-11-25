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

// withoutAt drops the wall-clock stamp so a test can compare the rest of an
// attention by value; At has its own tests with a pinned clock.
func withoutAt(a AgentAttention) AgentAttention {
	a.At = time.Time{}
	return a
}

func TestAttentionSetBumpsRevisionEveryTimeIncludingSameState(t *testing.T) {
	s := NewAttentionStore(nil)

	first := s.Set("a", AttentionFinished)
	second := s.Set("a", AttentionFinished)

	if first.Revision != 1 || second.Revision != 2 {
		t.Fatalf("revisions = %d, %d; want 1, 2", first.Revision, second.Revision)
	}
	got, ok := s.Get("a")
	if !ok || withoutAt(got) != (AgentAttention{State: AttentionFinished, Revision: 2}) {
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
	if !ok || withoutAt(got) != (AgentAttention{State: AttentionNeedsInput, Revision: 4}) {
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
	if p.Attention == nil || withoutAt(*p.Attention) != (AgentAttention{State: AttentionNeedsInput, Revision: 1}) {
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
	if p.Attention == nil || withoutAt(*p.Attention) != (AgentAttention{State: AttentionFinished, Revision: 1}) {
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

	if !ok || withoutAt(got) != (AgentAttention{State: AttentionErrored, Revision: 2}) {
		t.Fatalf("tracked = %+v, %v", got, ok)
	}
	if untrackedOK {
		t.Fatal("untracked agent transitioned")
	}
	if _, present := s.Get("untracked"); present {
		t.Fatal("SetIfTracked created an entry")
	}
}

func TestAttentionSetIfUntrackedKeepsExistingState(t *testing.T) {
	s := NewAttentionStore(nil)
	s.Set("tracked", AttentionWorking)

	_, trackedOK := s.SetIfUntracked("tracked", AttentionUnknown)
	got, ok := s.SetIfUntracked("untracked", AttentionUnknown)

	if trackedOK {
		t.Fatal("tracked agent was overwritten")
	}
	if att, _ := s.Get("tracked"); withoutAt(att) != (AgentAttention{State: AttentionWorking, Revision: 1}) {
		t.Fatalf("tracked = %+v, want working rev 1", att)
	}
	if !ok || withoutAt(got) != (AgentAttention{State: AttentionUnknown, Revision: 1}) {
		t.Fatalf("untracked = %+v, %v", got, ok)
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
	if att, ok := s.SetByToken("tok", AttentionWorking, AttentionNeedsInput); !ok || withoutAt(att) != (AgentAttention{State: AttentionWorking, Revision: 3}) {
		t.Fatalf("guarded transition from needs_input = %+v, %v", att, ok)
	}
}

func TestFinishedFromAHookCarriesItsPreviewUntilTheNextTransition(t *testing.T) {
	pub := &syncRecorder{}
	s := NewAttentionStore(pub)
	s.RegisterToken("tok", "a")

	att, ok := s.SetByTokenFinished("tok", "Fixed the bug.")
	if !ok || att.State != AttentionFinished || att.Preview != "Fixed the bug." {
		t.Fatalf("finished = %+v, %v; want the preview on the transition", att, ok)
	}
	if got, _ := s.Get("a"); got.Preview != "Fixed the bug." {
		t.Fatalf("Get = %+v, want the preview kept while finished", got)
	}

	working, _ := s.SetByToken("tok", AttentionWorking)
	if working.Preview != "" {
		t.Fatalf("working = %+v, want the preview gone", working)
	}
	again, _ := s.SetByTokenFinished("tok", "")
	if again.Preview != "" {
		t.Fatalf("a later finished without a preview inherited %q", again.Preview)
	}
}

func TestHeldFinishedCarriesNoPreview(t *testing.T) {
	s := NewAttentionStore(nil)
	s.RegisterToken("tok", "a")
	s.BindBridgeKey("k", "k-launch", "a")
	s.SetOutstandingSubagents("k", "k-launch", 1)
	s.Set("a", AttentionWorking)

	att, _ := s.SetByTokenFinished("tok", "not yet")

	if att.State != AttentionWorking || att.Preview != "" {
		t.Fatalf("held finished = %+v, want working without a preview", att)
	}
}

// steppedClock returns a clock that advances one minute per reading.
func steppedClock(start time.Time) func() time.Time {
	t := start
	return func() time.Time {
		now := t
		t = t.Add(time.Minute)
		return now
	}
}

func TestAttentionAtIsTheDaemonClockAtTheLastStateChange(t *testing.T) {
	start := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	s := NewAttentionStore(nil)
	s.SetClock(steppedClock(start))
	s.RegisterToken("tok", "a")

	working := s.Set("a", AttentionWorking)
	fin, _ := s.SetByTokenFinished("tok", "done")
	got, _ := s.Get("a")

	if !working.At.Equal(start) {
		t.Fatalf("working at = %v, want %v", working.At, start)
	}
	if !fin.At.Equal(start.Add(time.Minute)) || !got.At.Equal(fin.At) {
		t.Fatalf("finished at = %v, Get at = %v; want %v and no change on read", fin.At, got.At, start.Add(time.Minute))
	}
}

func TestAttentionAtSerializesAsRFC3339AndIsAbsentWhenUnset(t *testing.T) {
	at := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	b, _ := json.Marshal(AgentAttention{State: AttentionFinished, At: at})
	if !strings.Contains(string(b), `"at":"2026-11-24T12:00:00Z"`) {
		t.Fatalf("json = %s, want an RFC 3339 at", b)
	}
	b, _ = json.Marshal(AgentAttention{State: AttentionFinished})
	if strings.Contains(string(b), `"at"`) {
		t.Fatalf("json = %s, want at omitted when unset", b)
	}
}

func TestAttentionAtFollowsARename(t *testing.T) {
	start := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	s := NewAttentionStore(nil)
	s.SetClock(steppedClock(start))
	s.Set("a", AttentionFinished)

	s.Move("a", "b")

	if got, _ := s.Get("b"); !got.At.Equal(start) {
		t.Fatalf("at after Move = %v, want the original %v", got.At, start)
	}
}

func TestErroredFromAFailedTurnCarriesAClampedSummary(t *testing.T) {
	s := NewAttentionStore(nil)
	s.RegisterToken("tok", "a")

	att, ok := s.SetByTokenErrored("tok", "rate\nlimit   "+strings.Repeat("x", 5000))
	if !ok || att.State != AttentionErrored {
		t.Fatalf("errored = %+v, %v", att, ok)
	}
	if strings.ContainsAny(att.Preview, "\n") || !strings.HasPrefix(att.Preview, "rate limit ") || len([]rune(att.Preview)) > MaxTurnPreview {
		t.Fatalf("preview = %q, want a clamped single line", att.Preview)
	}
	if !att.TurnFailed() {
		t.Fatal("TurnFailed = false for an errored attention with a summary")
	}
	if next, _ := s.SetByToken("tok", AttentionWorking); next.Preview != "" || next.TurnFailed() {
		t.Fatalf("working = %+v, want the summary gone", next)
	}
}

func TestErroredFromALifecycleExitIsNotATurnFailure(t *testing.T) {
	s := NewAttentionStore(nil)
	if att := s.Set("a", AttentionErrored); att.TurnFailed() {
		t.Fatalf("lifecycle errored = %+v, want TurnFailed false", att)
	}
}

func TestErroredWithoutDetailStillSaysTheTurnFailed(t *testing.T) {
	s := NewAttentionStore(nil)
	s.RegisterToken("tok", "a")
	att, _ := s.SetByTokenErrored("tok", "  ")
	if att.Preview == "" || !att.TurnFailed() {
		t.Fatalf("errored = %+v, want a fallback summary so the failure stays distinguishable", att)
	}
}

// bridgedFailure is an agent whose launch is bound to a bridge key, so a
// turn.complete from the bridge and a StopFailure hook can both report on it.
func bridgedFailure(t *testing.T) (*AttentionStore, *syncRecorder) {
	t.Helper()
	pub := &syncRecorder{}
	s := NewAttentionStore(pub)
	s.RegisterToken("tok", "a")
	s.BindBridgeKey("k", "k-launch", "a")
	s.Set("a", AttentionWorking)
	pub.reset()
	return s, pub
}

func attentionStates(pub *syncRecorder) []AttentionState {
	var out []AttentionState
	for _, ev := range pub.snapshot() {
		if p, ok := ev.Payload.(*AgentActivityPayload); ok && p.Attention != nil {
			out = append(out, p.Attention.State)
		}
	}
	return out
}

// StopFailure first, then the bridge's turn.complete for the same turn: the
// failure stands, keeps its summary, and the completion publishes nothing.
func TestBridgeTurnCompleteAfterAFailedTurnDoesNotOverwriteIt(t *testing.T) {
	s, pub := bridgedFailure(t)
	s.SetByTokenErrored("tok", "rate limited")

	att, ok := s.CompleteBridgeTurn("k", "k-launch", 0)

	if ok {
		t.Fatalf("CompleteBridgeTurn published %+v over a failed turn", att)
	}
	got, _ := s.Get("a")
	if got.State != AttentionErrored || got.Preview != "rate limited" || !got.TurnFailed() {
		t.Fatalf("attention = %+v, want the failed turn kept with its summary", got)
	}
	if states := attentionStates(pub); len(states) != 1 || states[0] != AttentionErrored {
		t.Fatalf("published %v, want exactly the one errored", states)
	}
}

// turn.complete first, then StopFailure: errored wins the state.
func TestStopFailureAfterTheBridgeTurnCompleteWinsTheState(t *testing.T) {
	s, pub := bridgedFailure(t)
	s.CompleteBridgeTurn("k", "k-launch", 0)

	s.SetByTokenErrored("tok", "rate limited")
	_, again := s.CompleteBridgeTurn("k", "k-launch", 0) // a repeated completion changes nothing

	got, _ := s.Get("a")
	if got.State != AttentionErrored || !got.TurnFailed() || again {
		t.Fatalf("attention = %+v (repeat published: %v), want errored to win", got, again)
	}
	if states := attentionStates(pub); len(states) != 2 || states[0] != AttentionFinished || states[1] != AttentionErrored {
		t.Fatalf("published %v, want finished then errored (the finished was already out)", states)
	}
}

func TestANewBridgeTurnClearsAFailedTurn(t *testing.T) {
	s, _ := bridgedFailure(t)
	s.SetByTokenErrored("tok", "rate limited")

	att, ok := s.BeginBridgeTurn("k", "k-launch")

	if !ok || att.State != AttentionWorking || att.Preview != "" {
		t.Fatalf("turn.start = %+v, %v; want working with the failure gone", att, ok)
	}
}

// A harness-exit errored has no live turn behind it; it is not protected.
func TestBridgeFinishedStillOverwritesALifecycleErrored(t *testing.T) {
	s, _ := bridgedFailure(t)
	s.Set("a", AttentionErrored)

	if att, ok := s.CompleteBridgeTurn("k", "k-launch", 0); !ok || att.State != AttentionFinished {
		t.Fatalf("turn.complete = %+v, %v; want finished over a lifecycle errored", att, ok)
	}
}

func TestWithoutReasonContentDropsOnlyTheInputAndNeverMutates(t *testing.T) {
	in := &AgentAttention{State: AttentionNeedsInput, Revision: 3, Reason: &AttentionReason{Kind: AttentionReasonPermission, Tool: "Bash", Detail: "rm", Input: "rm -rf x"}}

	out := in.WithoutReasonContent()

	if out.Reason.Input != "" || out.Reason.Detail != "" || out.Reason.Kind != AttentionReasonPermission || out.Reason.Tool != "Bash" || out.Revision != 3 || out.State != AttentionNeedsInput {
		t.Fatalf("out = %+v / %+v, want input and detail dropped, the rest kept", out, out.Reason)
	}
	if in.Reason.Input != "rm -rf x" || in.Reason.Detail != "rm" {
		t.Fatal("WithoutReasonContent mutated the shared attention")
	}
	if (*AgentAttention)(nil).WithoutReasonContent() != nil {
		t.Fatal("nil attention must stay nil")
	}
	plain := &AgentAttention{State: AttentionFinished}
	if plain.WithoutReasonContent() != plain {
		t.Fatal("an attention without an input with no reason should be returned as is")
	}
}

func TestSnapshotWithoutAttentionContentLeavesTheOriginalIntact(t *testing.T) {
	in := Snapshot{Agents: []Agent{{Name: "a", Attention: &AgentAttention{Reason: &AttentionReason{Input: "secret", Detail: "asked"}}}, {Name: "b"}}}

	out := in.WithoutAttentionContent()

	if out.Agents[0].Attention.Reason.Input != "" || out.Agents[0].Attention.Reason.Detail != "" || in.Agents[0].Attention.Reason.Input != "secret" || in.Agents[0].Attention.Reason.Detail != "asked" || out.Agents[1].Attention != nil {
		t.Fatalf("out = %+v, in = %+v", out.Agents, in.Agents)
	}
}

func TestWithoutReasonContentDropsPreviewButKeepsStateAndOutstanding(t *testing.T) {
	in := &AgentAttention{State: AttentionErrored, Revision: 2, Preview: "boom: secret", Outstanding: &Outstanding{Subagents: 1}}

	out := in.WithoutReasonContent()

	if out.Preview != "" || out.State != AttentionErrored || out.Revision != 2 || out.Outstanding == nil || out.Outstanding.Subagents != 1 {
		t.Fatalf("out = %+v, want only the preview dropped", out)
	}
	if in.Preview != "boom: secret" {
		t.Fatal("redaction mutated the shared attention")
	}
}

func TestAgentWithoutContentDropsConversationTextAndNeverMutates(t *testing.T) {
	in := Agent{
		Name:          "a",
		Attention:     &AgentAttention{State: AttentionFinished, Preview: "final"},
		CurrentAction: &Action{Kind: ActionKindTool, Detail: "Bash rm"},
		Tasks:         []AgentTask{{ID: "t", Title: "title", Status: "status", Mode: TaskDeterminate}},
	}

	out := in.WithoutContent()

	if out.Attention.Preview != "" || out.CurrentAction.Detail != "" || out.CurrentAction.Kind != ActionKindTool {
		t.Fatalf("out = %+v / %+v", out.Attention, out.CurrentAction)
	}
	if got := out.Tasks[0]; got.Title != "" || got.Status != "" || got.ID != "t" || got.Mode != TaskDeterminate {
		t.Fatalf("task = %+v, want text dropped and the rest kept", got)
	}
	if in.Attention.Preview != "final" || in.CurrentAction.Detail != "Bash rm" || in.Tasks[0].Title != "title" || in.Tasks[0].Status != "status" {
		t.Fatal("redaction mutated the original agent")
	}
}
