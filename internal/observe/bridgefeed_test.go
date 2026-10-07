package observe

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

// feedHarness drives a BridgeFeed for agent "alice" (key "alice-key").
type feedHarness struct {
	pub       *syncRecorder
	store     *AttentionStore
	feed      *BridgeFeed
	clock     *fakeClock
	connected map[string]bool
}

func newFeedHarness(t *testing.T, keys staticKeys) *feedHarness {
	t.Helper()
	if keys == nil {
		keys = staticKeys{"alice-key": "alice"}
	}
	h := &feedHarness{pub: &syncRecorder{}, clock: newFakeClock(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)), connected: map[string]bool{}}
	h.store = NewAttentionStore(h.pub)
	h.store.Set("alice", AttentionUnknown)
	h.feed = NewBridgeFeed(keys, h.pub,
		WithFeedAttention(h.store),
		WithFeedClock(h.clock),
		WithFeedConnected(func(key string) bool { return h.connected[key] }))
	h.pub.reset()
	return h
}

func (h *feedHarness) send(ev bridge.Event) {
	if ev.Agent == "" {
		ev.Agent = "alice-key"
	}
	if ev.Gen == 0 {
		ev.Gen = 1
	}
	if ev.SessionID == "" {
		ev.SessionID = "s1"
	}
	ev.At = h.clock.Now()
	h.feed.OnBridgeEvent(ev)
}

func (h *feedHarness) ofType(typ EventType) []Event {
	var out []Event
	for _, ev := range h.pub.snapshot() {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func usageJSON(t *testing.T, cost float64, ctxTokens int64) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"startedAt": 1,
		"cost":      map[string]any{"usd": cost},
		"context":   map[string]any{"tokens": ctxTokens, "window": 200000, "percent": float64(ctxTokens) / 2000},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func tokens(in, out int64) *bridge.TurnTokens {
	return &bridge.TurnTokens{Input: in, Output: out, CacheRead: 10, CacheCreation: 5}
}

func TestBridgeFeedIgnoresUnresolvedKeys(t *testing.T) {
	h := newFeedHarness(t, nil)

	h.send(bridge.Event{Agent: "dispatch.d-1", Name: bridge.EventTurnStart})
	h.send(bridge.Event{Agent: "stranger", Name: bridge.EventTurnComplete, Tokens: tokens(1, 1)})

	if evs := h.pub.snapshot(); len(evs) != 0 {
		t.Fatalf("published %v for keys that are not agents", eventTypes(evs))
	}
	if agents := h.feed.BridgeAgents(); len(agents) != 0 {
		t.Fatalf("BridgeAgents = %v", agents)
	}
}

func TestBridgeFeedPublishesTurnLifecycle(t *testing.T) {
	h := newFeedHarness(t, nil)

	h.send(bridge.Event{Name: bridge.ReportHello})
	h.send(bridge.Event{Name: bridge.EventTurnStart, EventID: "ts1", Prompt: "secret prompt"})
	h.send(bridge.Event{
		Name: bridge.EventTurnComplete, EventID: "tc1",
		Message: "line one\n\x1b[1mline two\x1b[0m " + strings.Repeat("x", 400),
		Tokens:  tokens(100, 20), Usage: usageJSON(t, 0.25, 4000),
	})

	started := h.ofType(EventAgentTurnStarted)
	if len(started) != 1 || *started[0].Payload.(*AgentTurnStartedPayload) != (AgentTurnStartedPayload{Agent: "alice", SessionID: "s1"}) {
		t.Fatalf("turn started = %+v", started)
	}
	completed := h.ofType(EventAgentTurnCompleted)
	if len(completed) != 1 {
		t.Fatalf("turn completed = %d events", len(completed))
	}
	p := completed[0].Payload.(*AgentTurnCompletedPayload)
	if p.Agent != "alice" || p.SessionID != "s1" || p.Outcome != TurnCompleted {
		t.Fatalf("completed = %+v", p)
	}
	if !strings.HasPrefix(p.Preview, "line one line two xxx") || len([]rune(p.Preview)) != MaxTurnPreview {
		t.Fatalf("preview = %q", p.Preview)
	}
	if p.Tokens != (TurnTokens{Input: 100, Output: 20, CacheRead: 10, CacheCreation: 5}) {
		t.Fatalf("tokens = %+v", p.Tokens)
	}
	if p.CostUSD == nil || *p.CostUSD != 0.25 {
		t.Fatalf("cost = %v", p.CostUSD)
	}
	if p.Context == nil || *p.Context != (ContextUsage{Tokens: 4000, Window: 200000, Percent: 2}) {
		t.Fatalf("context = %+v", p.Context)
	}
	if got, _ := h.store.Get("alice"); got.State != AttentionFinished {
		t.Fatalf("attention = %s; want finished", got.State)
	}
	if usage := h.ofType(EventAgentUsage); len(usage) != 0 {
		t.Fatalf("agent_usage published alongside a turn completion: %d", len(usage))
	}
}

func TestBridgeFeedAbortedTurnOutcome(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "tc1", Reason: "aborted"})

	completed := h.ofType(EventAgentTurnCompleted)
	if len(completed) != 1 || completed[0].Payload.(*AgentTurnCompletedPayload).Outcome != TurnAborted {
		t.Fatalf("completed = %+v", completed)
	}
}

func TestBridgeFeedReplayedEventCountsOnce(t *testing.T) {
	h := newFeedHarness(t, nil)
	ev := bridge.Event{Name: bridge.EventTurnComplete, EventID: "tc1", Tokens: tokens(100, 20), Usage: usageJSON(t, 0.25, 10)}

	h.send(ev)
	h.send(ev)

	if n := len(h.ofType(EventAgentTurnCompleted)); n != 1 {
		t.Fatalf("published %d turn completions for one event id", n)
	}
	u := h.feed.BridgeAgents()["alice"].Usage
	if u == nil || u.Session.Tokens != 135 || u.Incarnation.Tokens != 135 {
		t.Fatalf("usage = %+v", u)
	}
}

func TestBridgeFeedUsageResetsPerSessionAndIncarnation(t *testing.T) {
	h := newFeedHarness(t, nil)

	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "a", Tokens: tokens(100, 20), Usage: usageJSON(t, 0.25, 10)})
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "b", Tokens: tokens(100, 20), Usage: usageJSON(t, 0.40, 20)})
	u := h.feed.BridgeAgents()["alice"].Usage
	if u.Session != (UsageTotals{Tokens: 270, CostUSD: 0.40}) || u.Incarnation != u.Session {
		t.Fatalf("usage after two turns = %+v", u)
	}
	if cost := h.ofType(EventAgentTurnCompleted)[1].Payload.(*AgentTurnCompletedPayload).CostUSD; cost == nil || !near(*cost, 0.15) {
		t.Fatalf("second turn cost = %v; want the delta", cost)
	}

	// /clear: a new session in the same process.
	h.send(bridge.Event{Name: bridge.ReportHello, SessionID: "s2"})
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "c", SessionID: "s2", Tokens: tokens(100, 20), Usage: usageJSON(t, 0.10, 5)})
	u = h.feed.BridgeAgents()["alice"].Usage
	if u.SessionID != "s2" || u.Session != (UsageTotals{Tokens: 135, CostUSD: 0.10}) {
		t.Fatalf("session after /clear = %+v", u.Session)
	}
	if u.Incarnation.Tokens != 405 || !near(u.Incarnation.CostUSD, 0.50) {
		t.Fatalf("incarnation after /clear = %+v", u.Incarnation)
	}

	// Respawn: a new generation.
	h.send(bridge.Event{Name: bridge.ReportHello, Gen: 2, SessionID: "s2"})
	u = h.feed.BridgeAgents()["alice"].Usage
	if u.Incarnation != (UsageTotals{}) {
		t.Fatalf("incarnation after respawn = %+v", u.Incarnation)
	}
	if n := len(h.ofType(EventAgentUsage)); n != 2 {
		t.Fatalf("agent_usage events = %d; want one per reset", n)
	}
}

func TestBridgeFeedUsageOutsideTurnPublishesAgentUsage(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "a", Tokens: tokens(1, 1), Usage: usageJSON(t, 0.25, 10)})

	h.send(bridge.Event{Name: bridge.EventSessionEnd, EventID: "e", Usage: usageJSON(t, 0.30, 0)})
	h.send(bridge.Event{Name: bridge.EventSessionEnd, EventID: "e2", Usage: usageJSON(t, 0.30, 0)})

	usage := h.ofType(EventAgentUsage)
	if len(usage) != 1 {
		t.Fatalf("agent_usage events = %d; want 1 (second report is unchanged)", len(usage))
	}
	if got := usage[0].Payload.(*AgentUsagePayload).Usage.Session.CostUSD; got != 0.30 {
		t.Fatalf("cost = %v", got)
	}
}

func near(a, b float64) bool { return a-b < 1e-9 && b-a < 1e-9 }

func TestBridgeFeedActivityIsCoalescedWithTrailingEdge(t *testing.T) {
	h := newFeedHarness(t, nil)
	activity := func(tool, summary string) {
		h.send(bridge.Event{Name: bridge.EventActivity, Activity: &bridge.ActivityReport{Tool: tool, Summary: summary}})
	}

	activity("Read", "~/a.go")
	h.clock.Advance(200 * time.Millisecond)
	activity("Bash", "go")
	h.clock.Advance(200 * time.Millisecond)
	activity("Edit", "~/b.go\n\x1b[31m"+strings.Repeat("y", 300))

	if n := len(h.ofType(EventAgentActivity)); n != 1 {
		t.Fatalf("published %d activity events inside one interval; want the leading edge only", n)
	}
	h.clock.Advance(600 * time.Millisecond)

	evs := h.ofType(EventAgentActivity)
	if len(evs) != 2 {
		t.Fatalf("published %d activity events; want leading + trailing", len(evs))
	}
	first := evs[0].Payload.(*AgentActivityPayload)
	if first.CurrentAction == nil || *first.CurrentAction != (Action{Kind: ActionKindTool, Detail: "Read ~/a.go"}) {
		t.Fatalf("leading action = %+v", first.CurrentAction)
	}
	last := evs[1].Payload.(*AgentActivityPayload)
	if last.CurrentAction == nil || last.CurrentAction.Kind != ActionKindTool || !strings.HasPrefix(last.CurrentAction.Detail, "Edit ~/b.go yyy") || len([]rune(last.CurrentAction.Detail)) != MaxActionDetail {
		t.Fatalf("trailing action = %+v", last.CurrentAction)
	}
	if last.Attention == nil {
		t.Fatal("activity event dropped the agent's attention")
	}
	if h.clock.pendingTimers() != 0 {
		t.Fatal("a timer is still armed after the trailing edge")
	}

	// Quiet for an interval: the next report goes out at once, and an
	// empty tool clears the action.
	h.clock.Advance(2 * time.Second)
	activity("", "")
	evs = h.ofType(EventAgentActivity)
	if len(evs) != 3 || evs[2].Payload.(*AgentActivityPayload).CurrentAction != nil {
		t.Fatalf("clear = %d events, last %+v", len(evs), evs[len(evs)-1].Payload)
	}
}

func TestBridgeFeedActivityBeforeTurnStartIsKept(t *testing.T) {
	h := newFeedHarness(t, nil)

	h.send(bridge.Event{Name: bridge.EventActivity, Activity: &bridge.ActivityReport{Tool: "Bash", Summary: "make"}})
	h.send(bridge.Event{Name: bridge.EventTurnStart, EventID: "ts1"})

	got := h.feed.BridgeAgents()["alice"].CurrentAction
	if got == nil || got.Detail != "Bash make" {
		t.Fatalf("current action = %+v", got)
	}
	if att, _ := h.store.Get("alice"); att.State != AttentionWorking {
		t.Fatalf("attention = %s", att.State)
	}
}

func TestBridgeFeedTurnCompleteClearsCurrentTool(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.EventActivity, Activity: &bridge.ActivityReport{Tool: "Bash"}})
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "tc1"})

	if got := h.feed.BridgeAgents()["alice"].CurrentAction; got != nil {
		t.Fatalf("current action = %+v after the turn", got)
	}
}

func TestBridgeFeedSubagentCountResetByModReloadReleasesHold(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.EventTurnStart, EventID: "ts1"})
	h.send(bridge.Event{Name: bridge.EventSubagents, Subagents: &bridge.SubagentsReport{Running: 2}})
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "tc1"})

	// The mod hot-reloads: a fresh hello, its count starting over.
	h.send(bridge.Event{Name: bridge.ReportHello})

	if got := h.feed.BridgeAgents()["alice"].Subagents; got != 0 {
		t.Fatalf("subagents = %d after reload", got)
	}
	if att, _ := h.store.Get("alice"); att.State != AttentionFinished || att.Outstanding != nil {
		t.Fatalf("attention = %+v; want finished", att)
	}
}

func TestBridgeFeedAttentionReports(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.EventTurnStart, EventID: "ts1"})

	h.send(bridge.Event{Name: bridge.EventAttention, Attention: &bridge.AttentionReport{State: bridge.AttentionNeedsInput, Kind: bridge.AttentionPermission, Tool: "Bash", Summary: "rm"}})

	att, _ := h.store.Get("alice")
	want := AttentionReason{Kind: AttentionReasonPermission, Tool: "Bash", Detail: "rm"}
	if att.State != AttentionNeedsInput || att.Reason == nil || *att.Reason != want {
		t.Fatalf("attention = %+v", att)
	}
	if got := h.feed.BridgeAgents()["alice"].Reason; got == nil || *got != want {
		t.Fatalf("feed reason = %+v", got)
	}

	h.send(bridge.Event{Name: bridge.EventAttention, Attention: &bridge.AttentionReport{State: bridge.AttentionCleared}})

	att, _ = h.store.Get("alice")
	if att.State != AttentionWorking || att.Reason != nil {
		t.Fatalf("attention after cleared = %+v", att)
	}
	if got := h.feed.BridgeAgents()["alice"].Reason; got != nil {
		t.Fatalf("feed reason after cleared = %+v", got)
	}
}

func TestBridgeFeedCompactionAndSessionEnd(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "tc1", Usage: usageJSON(t, 0.1, 150000)})

	h.send(bridge.Event{Name: bridge.EventCompact, Compact: &bridge.CompactReport{Phase: bridge.CompactStarted, Trigger: bridge.CompactAuto}})
	h.send(bridge.Event{Name: bridge.EventSessionEnd, EventID: "se1", Reason: "clear\nnow"})

	compact := h.ofType(EventAgentCompaction)
	if len(compact) != 1 {
		t.Fatalf("compaction events = %d", len(compact))
	}
	cp := compact[0].Payload.(*AgentCompactionPayload)
	if cp.Agent != "alice" || cp.Phase != CompactionStarted || cp.Trigger != CompactionAuto || cp.ContextPercent == nil || *cp.ContextPercent != 75 {
		t.Fatalf("compaction = %+v", cp)
	}
	ended := h.ofType(EventAgentSessionEnded)
	if len(ended) != 1 || *ended[0].Payload.(*AgentSessionEndedPayload) != (AgentSessionEndedPayload{Agent: "alice", SessionID: "s1", Reason: "clear now"}) {
		t.Fatalf("session ended = %+v", ended)
	}
}

func TestBridgeFeedBridgeAgentsReportsLinkState(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.ReportHello})

	h.connected["alice-key"] = true
	if got := h.feed.BridgeAgents()["alice"].Bridge; got != BridgeConnected {
		t.Fatalf("bridge = %q; want connected", got)
	}
	h.connected["alice-key"] = false
	if got := h.feed.BridgeAgents()["alice"].Bridge; got != BridgeAbsent {
		t.Fatalf("bridge = %q; want absent", got)
	}
}

func TestBridgeFeedFollowsRename(t *testing.T) {
	keys := staticKeys{"alice-key": "alice"}
	h := newFeedHarness(t, keys)
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "a", Tokens: tokens(100, 20)})

	delete(keys, "alice-key")
	keys["alice-key"] = "alicia"
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "b", Tokens: tokens(100, 20)})

	agents := h.feed.BridgeAgents()
	if _, stale := agents["alice"]; stale {
		t.Fatal("state left under the old name")
	}
	if u := agents["alicia"].Usage; u == nil || u.Session.Tokens != 270 {
		t.Fatalf("usage under new name = %+v", u)
	}
}

func TestBridgeFeedNilSafe(t *testing.T) {
	var f *BridgeFeed
	f.OnBridgeEvent(bridge.Event{Name: bridge.EventTurnStart})
	if got := f.BridgeAgents(); len(got) != 0 {
		t.Fatalf("nil feed BridgeAgents = %v", got)
	}
}
