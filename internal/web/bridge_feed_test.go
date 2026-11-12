package web

import (
	"bytes"
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
	store.BindBridgeKey(key, bridge.LaunchID(key, "launch-1"), name) // as the supervisor does at launch
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

// While the bridge is connected it alone says what the agent runs: no tool
// (nil) clears a stale pane action. A disconnected bridge leaves the pane's.
func TestBridgeConnectedActionIsAuthoritativeEvenWhenNil(t *testing.T) {
	pane := &observe.Action{Kind: observe.ActionKindPane, Detail: "stale spinner"}
	activity := staticActivityProvider{"agent-a": {Activity: observe.ActivityWorking, CurrentAction: pane}, "agent-b": {Activity: observe.ActivityWorking, CurrentAction: pane}}
	src := AgentSources{
		Activity: activity,
		BridgeFeed: fakeBridgeFeed{
			"agent-a": {Bridge: observe.BridgeConnected},
			"agent-b": {Bridge: observe.BridgeAbsent},
		},
	}
	records := []agent.Record{{Name: "agent-a"}, {Name: "agent-b"}}

	for name, agents := range map[string][]observe.Agent{
		"ProjectAgents": ProjectAgents(records, nil, src, nil),
		"buildSnapshot": buildSnapshot(snapshotInput{Records: records, Activity: activity, BridgeFeed: src.BridgeFeed, Now: time.Now()}).Agents,
	} {
		if agents[0].CurrentAction != nil {
			t.Errorf("%s: connected agent-a action = %+v; want nil", name, agents[0].CurrentAction)
		}
		if agents[1].CurrentAction == nil || agents[1].CurrentAction.Detail != "stale spinner" {
			t.Errorf("%s: absent agent-b action = %+v; want the pane's", name, agents[1].CurrentAction)
		}
	}
}

type staticActivityProvider map[string]observe.AgentActivity

func (s staticActivityProvider) Activities() map[string]observe.AgentActivity { return s }

// The state payload carries the richer usage fields, and the observed model
// sits beside Agent.Model (config-derived) without replacing it.
func TestBuildSnapshotCarriesObservedUsageFields(t *testing.T) {
	cfg := bridgeMergeConfig()
	cfg.Templates["claude"] = config.TemplateConfig{Harness: "claude", Model: "opus"}
	feed := fakeBridgeFeed{"agent-a": {
		Bridge: observe.BridgeConnected,
		Usage: &observe.AgentUsage{
			SessionID: "s1",
			Model:     "claude-opus-5-5",
			Effort:    "high",
			Context: &observe.ContextUsage{
				Tokens: 5, Window: 10, Percent: 50, CompactAt: 0.8,
				Breakdown: observe.ContextBreakdown{System: 1, Tools: 2, MCP: 3, Memory: 4, Messages: 5},
			},
			RateLimits: []observe.RateLimit{{Kind: observe.RateLimitSevenDay, PercentUsed: 12, ResetsAt: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}},
			Git:        &observe.GitStats{Added: 7, Removed: 3},
		},
	}}

	snap := buildSnapshot(snapshotInput{Config: cfg, Records: bridgeMergeRecords(), BridgeFeed: feed, Now: time.Now()})

	var a observe.Agent
	for _, cand := range snap.Agents {
		if cand.Name == "agent-a" {
			a = cand
		}
	}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Model string `json:"model"`
		Usage struct {
			Model   string `json:"model"`
			Effort  string `json:"effort"`
			Context struct {
				Breakdown map[string]int `json:"breakdown"`
				CompactAt float64        `json:"compact_at"`
			} `json:"context"`
			RateLimits []struct {
				Kind        string  `json:"kind"`
				PercentUsed float64 `json:"percent_used"`
				ResetsAt    string  `json:"resets_at"`
			} `json:"rate_limits"`
			Git map[string]int `json:"git"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Model != "opus" || wire.Usage.Model != "claude-opus-5-5" || wire.Usage.Effort != "high" {
		t.Fatalf("model=%q usage.model=%q effort=%q", wire.Model, wire.Usage.Model, wire.Usage.Effort)
	}
	if wire.Usage.Context.CompactAt != 0.8 || wire.Usage.Context.Breakdown["mcp"] != 3 || wire.Usage.Context.Breakdown["messages"] != 5 {
		t.Fatalf("context = %+v", wire.Usage.Context)
	}
	if len(wire.Usage.RateLimits) != 1 || wire.Usage.RateLimits[0].Kind != "seven_day" || wire.Usage.RateLimits[0].ResetsAt != "2026-11-17T10:00:00Z" {
		t.Fatalf("rate limits = %+v", wire.Usage.RateLimits)
	}
	if wire.Usage.Git["added"] != 7 || wire.Usage.Git["removed"] != 3 {
		t.Fatalf("git = %+v", wire.Usage.Git)
	}
}

func TestAgentGitListerSkipsAgentsWithoutALiveWorkspace(t *testing.T) {
	l := agentGitLister{list: func() []agent.Record {
		return []agent.Record{
			{Name: "live", Status: "running", Workspace: "/ws/live"},
			{Name: "suspended", Status: "suspended", Workspace: "/ws/s"},
			{Name: "stopped", Status: "stopped", Workspace: "/ws/x"},
			{Name: "noworkspace", Status: "running"},
		}
	}}
	got := l.GitAgents()
	if len(got) != 1 || got[0] != (observe.GitAgent{Name: "live", Workspace: "/ws/live"}) {
		t.Fatalf("agents = %+v", got)
	}
}

// A codex agent's harness-reported usage reaches the snapshot through the
// real feed: usage present, no cost, and no bridge field.
func TestBuildSnapshotCodexUsageFromHarness(t *testing.T) {
	feed := observe.NewBridgeFeed(observe.KeyResolverFunc(func(string) (string, bool) { return "", false }), nil)
	feed.SetHarnessUsage(map[string]observe.AgentUsage{"agent-c": {
		SessionID:   "sid",
		Session:     observe.UsageTotals{Tokens: 42, CostUnknown: true},
		Incarnation: observe.UsageTotals{Tokens: 42, CostUnknown: true},
		Model:       "gpt-test-1",
	}})

	snap := buildSnapshot(snapshotInput{Config: bridgeMergeConfig(), Records: bridgeMergeRecords(), BridgeFeed: feed, Now: time.Now()})

	var got observe.Agent
	for _, a := range snap.Agents {
		if a.Name == "agent-c" {
			got = a
		}
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if got.Usage == nil || got.Usage.Session.Tokens != 42 || got.Usage.Model != "gpt-test-1" {
		t.Fatalf("usage = %+v", got.Usage)
	}
	if bytes.Contains(raw, []byte(`"cost_usd"`)) || bytes.Contains(raw, []byte(`"bridge"`)) {
		t.Fatalf("codex agent carries cost_usd or bridge: %s", raw)
	}
}
