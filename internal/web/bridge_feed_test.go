package web

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// lockedRecorder is an observe.Publisher recording every event.
type lockedRecorder struct {
	mu     sync.Mutex
	events []observe.Event
}

func (r *lockedRecorder) Publish(ev observe.Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *lockedRecorder) types() []observe.EventType {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]observe.EventType, 0, len(r.events))
	for _, ev := range r.events {
		out = append(out, ev.Type)
	}
	return out
}

// fakeBridgeFeed is a test double for bridgeFeedProvider.
type fakeBridgeFeed map[string]observe.BridgeAgentState

func (f fakeBridgeFeed) BridgeAgents() map[string]observe.BridgeAgentState { return f }

// countingDispatches is a dispatchProvider with fixed outstanding counts.
type countingDispatches map[string]int

func (c countingDispatches) OutstandingDispatches() map[string]int   { return c }
func (c countingDispatches) Dispatches(time.Time) []observe.Dispatch { return nil }

func bridgeMergeConfig() *config.Config {
	return &config.Config{Templates: map[string]config.TemplateConfig{
		"claude": {Harness: "claude"},
		"codex":  {Harness: "codex", Model: "gpt-5"},
	}}
}

func bridgeMergeSources(store *observe.AttentionStore) AgentSources {
	return AgentSources{
		Attention: store,
		BridgeFeed: fakeBridgeFeed{"agent-a": {
			Bridge:        observe.BridgeConnected,
			Usage:         &observe.AgentUsage{SessionID: "s1", Session: observe.UsageTotals{Tokens: 10, CostUSD: 0.5}},
			Subagents:     1,
			CurrentAction: &observe.Action{Kind: observe.ActionKindTool, Detail: "Bash make"},
		}},
		Dispatches: countingDispatches{"agent-a": 2, "agent-b": 1},
	}
}

func bridgeMergeRecords() []agent.Record {
	return []agent.Record{
		{Name: "agent-a", Template: "claude"},
		{Name: "agent-b", Template: "claude"},
		{Name: "agent-c", Template: "codex"},
	}
}

func assertBridgeMerge(t *testing.T, agents []observe.Agent) {
	t.Helper()
	byName := map[string]observe.Agent{}
	for _, a := range agents {
		byName[a.Name] = a
	}
	a := byName["agent-a"]
	if a.Bridge != observe.BridgeConnected || a.Usage == nil || a.Usage.Session.Tokens != 10 {
		t.Fatalf("agent-a bridge/usage = %q %+v", a.Bridge, a.Usage)
	}
	if a.Outstanding == nil || *a.Outstanding != (observe.Outstanding{Dispatches: 2, Subagents: 1}) {
		t.Fatalf("agent-a outstanding = %+v", a.Outstanding)
	}
	if a.CurrentAction == nil || a.CurrentAction.Kind != observe.ActionKindTool {
		t.Fatalf("agent-a current action = %+v; want the bridge's tool", a.CurrentAction)
	}
	b := byName["agent-b"]
	if b.Bridge != observe.BridgeAbsent || b.Usage != nil {
		t.Fatalf("agent-b (claude, no bridge reading) = %q %+v; want absent", b.Bridge, b.Usage)
	}
	if b.Outstanding == nil || *b.Outstanding != (observe.Outstanding{Dispatches: 1}) {
		t.Fatalf("agent-b outstanding = %+v", b.Outstanding)
	}
	c := byName["agent-c"]
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"bridge"`, `"usage"`, `"outstanding"`} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("agent-c (codex) JSON %s carries %s", raw, key)
		}
	}
}

func TestBuildSnapshotMergesBridgeFeedAndOutstanding(t *testing.T) {
	src := bridgeMergeSources(nil)
	snap := buildSnapshot(snapshotInput{
		Config: bridgeMergeConfig(), Records: bridgeMergeRecords(),
		BridgeFeed: src.BridgeFeed, Dispatches: src.Dispatches.(countingDispatches), Now: time.Now(),
	})
	assertBridgeMerge(t, snap.Agents)
}

func TestProjectAgentsMergesBridgeFeedAndOutstanding(t *testing.T) {
	agents := ProjectAgents(bridgeMergeRecords(), nil, bridgeMergeSources(nil), bridgeMergeConfig())
	assertBridgeMerge(t, agents)
}

func TestSetupConsultRuntimeFeedsBridgeEventsIntoAttention(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t) // agent "leo-coding-leo"
	const name, key = "leo-coding-leo", "lcl-key"
	pub := &lockedRecorder{}
	store := observe.NewAttentionStore(pub)
	store.Set(name, observe.AttentionUnknown)
	s.attention, s.publisher, s.bridgeFeed = store, pub, nil

	hub := bridge.New(bridge.Options{})
	t.Cleanup(hub.Close)
	target, err := hub.Open(key, "launch-1")
	if err != nil {
		t.Fatal(err)
	}
	router := &bridge.Router{Hub: hub, Targets: func(agent string) (bridge.Target, bool) {
		return target, agent == name
	}}
	s.setupConsultRuntime(Options{Bridge: BridgeOptions{Router: router}}, nil)
	if s.bridgeFeed == nil {
		t.Fatal("bridge feed not wired")
	}
	if _, err := hub.Open("dispatch.d-1", "launch-d"); err != nil {
		t.Fatal(err)
	}
	apply := func(agent, launch string, r bridge.Report) {
		t.Helper()
		if err := hub.Apply(agent, launch, r); err != nil {
			t.Fatalf("apply %s %s: %v", r.Type, r.Name, err)
		}
	}
	event := func(name, id string) bridge.Report {
		return bridge.Report{Type: bridge.ReportEvent, Name: name, EventID: id}
	}
	subagents := func(n int) bridge.Report {
		r := event(bridge.EventSubagents, "")
		r.Subagents = &bridge.SubagentsReport{Running: n}
		return r
	}

	apply(key, "launch-1", bridge.Report{Type: bridge.ReportHello, SessionID: "s1"})
	apply(key, "launch-1", event(bridge.EventTurnStart, "t1"))
	apply(key, "launch-1", subagents(1))
	apply(key, "launch-1", event(bridge.EventTurnComplete, "c1"))
	// A dispatch's own turn is not the agent's.
	apply("dispatch.d-1", "launch-d", bridge.Report{Type: bridge.ReportHello, SessionID: "d"})
	apply("dispatch.d-1", "launch-d", event(bridge.EventTurnComplete, "dc1"))

	if att, _ := store.Get(name); att.State != observe.AttentionWorking || att.Outstanding == nil || att.Outstanding.Subagents != 1 {
		t.Fatalf("after turn.complete with a subagent: %+v", att)
	}
	apply(key, "launch-1", subagents(0))
	if att, _ := store.Get(name); att.State != observe.AttentionFinished {
		t.Fatalf("after the subagent stopped: %+v", att)
	}

	agents := s.bridgeFeed.BridgeAgents()
	if _, ok := agents[name]; !ok || len(agents) != 1 {
		t.Fatalf("BridgeAgents = %+v; want only %s", agents, name)
	}
	completed := 0
	for _, typ := range pub.types() {
		if typ == observe.EventAgentTurnCompleted {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("published %d turn completions; want the agent's one (events %v)", completed, pub.types())
	}
}
