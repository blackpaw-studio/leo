package observe

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

// hostUsage is the shape of the host's $.session.usage({breakdown:'summary'})
// result plus the model id the mod adds. Callers patch fields over it.
func hostUsage(t *testing.T, patch map[string]any) json.RawMessage {
	t.Helper()
	u := map[string]any{
		"startedAt": 1,
		"cost":      map[string]any{"usd": 0.25},
		"model":     "claude-opus-5-5",
		"context": map[string]any{
			"tokens": 4000, "window": 200000, "percent": 2,
			"breakdown": map[string]any{
				"categories": []map[string]any{
					{"name": "System prompt", "tokens": 3000, "kind": "used"},
					{"name": "System tools", "tokens": 9000, "kind": "used"},
					{"name": "Skills", "tokens": 500, "kind": "used"},
					{"name": "Custom agents", "tokens": 250, "kind": "used"},
					{"name": "Slash commands", "tokens": 50, "kind": "used"},
					{"name": "MCP tools", "tokens": 7000, "kind": "deferred"},
					{"name": "Memory files", "tokens": 1200, "kind": "used"},
					{"name": "Messages", "tokens": 4000, "kind": "used"},
					{"name": "Free space", "tokens": 150000, "kind": "free"},
					{"name": "Autocompact buffer", "tokens": 33000, "kind": "reserved"},
					{"name": "Brand new thing", "tokens": 11, "kind": "used"},
				},
				"rawMaxTokens": 200000, "autoCompactThreshold": 160000, "isAutoCompactEnabled": true,
			},
		},
		"rateLimits": []map[string]any{
			{"kind": "five_hour", "percentUsed": 41.5, "resetsAt": "2026-11-12T05:00:00-05:00"},
			{"kind": "seven_day", "percentUsed": 12, "resetsAt": "2026-11-17T10:00:00Z"},
		},
	}
	for k, v := range patch {
		if v == nil {
			delete(u, k)
		} else {
			u[k] = v
		}
	}
	raw, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (h *feedHarness) usageNow(t *testing.T) AgentUsage {
	t.Helper()
	u := h.feed.BridgeAgents()["alice"].Usage
	if u == nil {
		t.Fatal("no usage")
	}
	return *u
}

func TestFoldUsageBreakdownRateLimitsModel(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "a", Usage: hostUsage(t, nil)})

	u := h.usageNow(t)
	// Only used rows count; unknown names fold into tools so the sum holds.
	wantBreakdown := ContextBreakdown{System: 3000, Tools: 9000 + 500 + 250 + 50 + 11, MCP: 0, Memory: 1200, Messages: 4000}
	if u.Context == nil || u.Context.Breakdown != wantBreakdown {
		t.Fatalf("breakdown = %+v; want %+v", u.Context, wantBreakdown)
	}
	if u.Context.CompactAt != 0.8 {
		t.Fatalf("compact_at = %v; want 160000/200000", u.Context.CompactAt)
	}
	if u.Model != "claude-opus-5-5" {
		t.Fatalf("model = %q", u.Model)
	}
	want := []RateLimit{
		{Kind: RateLimitFiveHour, PercentUsed: 41.5, ResetsAt: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)},
		{Kind: RateLimitSevenDay, PercentUsed: 12, ResetsAt: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)},
	}
	if len(u.RateLimits) != 2 || u.RateLimits[0] != want[0] || u.RateLimits[1] != want[1] {
		t.Fatalf("rate limits = %+v; want %+v", u.RateLimits, want)
	}
	if u.RateLimits[0].ResetsAt.Location() != time.UTC {
		t.Fatalf("resets_at not UTC: %v", u.RateLimits[0].ResetsAt.Location())
	}
}

func TestFoldUsageCompactAt(t *testing.T) {
	cases := []struct {
		name     string
		bd       map[string]any
		window   int
		wantAt   float64
		wantDump string
	}{
		{"enabled", map[string]any{"autoCompactThreshold": 100000, "isAutoCompactEnabled": true}, 200000, 0.5, ""},
		{"disabled", map[string]any{"autoCompactThreshold": 100000, "isAutoCompactEnabled": false}, 200000, 0, ""},
		{"threshold absent", map[string]any{"isAutoCompactEnabled": true}, 200000, 0, ""},
		{"threshold zero", map[string]any{"autoCompactThreshold": 0, "isAutoCompactEnabled": true}, 200000, 0, ""},
		{"window zero", map[string]any{"autoCompactThreshold": 100, "isAutoCompactEnabled": true}, 0, 0, ""},
		{"threshold above window", map[string]any{"autoCompactThreshold": 300000, "isAutoCompactEnabled": true}, 200000, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newFeedHarness(t, nil)
			bd := map[string]any{"categories": []map[string]any{{"name": "Messages", "tokens": 5, "kind": "used"}}, "rawMaxTokens": tc.window}
			for k, v := range tc.bd {
				bd[k] = v
			}
			ctx := map[string]any{"tokens": 5, "window": tc.window, "percent": 1, "breakdown": bd}
			h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "a", Usage: hostUsage(t, map[string]any{"context": ctx})})
			if got := h.usageNow(t).Context.CompactAt; got != tc.wantAt {
				t.Fatalf("compact_at = %v; want %v", got, tc.wantAt)
			}
		})
	}
}

func TestFoldUsageRateLimitsDropBadEntries(t *testing.T) {
	limits := []map[string]any{
		{"kind": "five_hour", "percentUsed": 10, "resetsAt": "2026-11-12T05:00:00Z"},
		{"kind": "monthly", "percentUsed": 10, "resetsAt": "2026-11-12T05:00:00Z"},   // unknown kind
		{"kind": "seven_day", "resetsAt": "2026-11-12T05:00:00Z"},                    // no percent
		{"kind": "seven_day", "percentUsed": -3, "resetsAt": "2026-11-12T05:00:00Z"}, // invalid percent
		{"kind": "seven_day", "percentUsed": 33, "resetsAt": 1794888000000},          // epoch ms
	}
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "a", Usage: hostUsage(t, map[string]any{"rateLimits": limits})})
	got := h.usageNow(t).RateLimits
	if len(got) != 2 || got[0].Kind != RateLimitFiveHour || got[1].Kind != RateLimitSevenDay || got[1].PercentUsed != 33 {
		t.Fatalf("rate limits = %+v", got)
	}
	if want := time.UnixMilli(1794888000000).UTC(); !got[1].ResetsAt.Equal(want) {
		t.Fatalf("epoch ms reset = %v; want %v", got[1].ResetsAt, want)
	}
}

// A limit whose reset time is missing or unreadable still reports its percent.
func TestFoldUsageRateLimitWithoutUsableResetKeepsPercent(t *testing.T) {
	limits := []map[string]any{
		{"kind": "five_hour", "percentUsed": 7, "resetsAt": "not a time"},
		{"kind": "seven_day", "percentUsed": 9},
	}
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.EventSessionEnd, EventID: "a", Usage: hostUsage(t, map[string]any{"rateLimits": limits})})
	got := h.usageNow(t).RateLimits
	if len(got) != 2 || got[0].PercentUsed != 7 || got[1].PercentUsed != 9 || !got[0].ResetsAt.IsZero() || !got[1].ResetsAt.IsZero() {
		t.Fatalf("rate limits = %+v", got)
	}
	raw, err := json.Marshal(h.ofType(EventAgentUsage)[0].Payload.(*AgentUsagePayload).Usage.RateLimits)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "resets_at") {
		t.Fatalf("wire %s carries a zero resets_at", raw)
	}
}

func TestFoldUsageRateLimitsAbsentKeepsLastEmptyClears(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "a", Usage: hostUsage(t, nil)})
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "b", Usage: hostUsage(t, map[string]any{"rateLimits": nil})})
	if n := len(h.usageNow(t).RateLimits); n != 2 {
		t.Fatalf("absent rateLimits dropped the last reading: %d", n)
	}
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "c", Usage: hostUsage(t, map[string]any{"rateLimits": []any{}})})
	if n := len(h.usageNow(t).RateLimits); n != 0 {
		t.Fatalf("empty rateLimits kept %d entries; want cleared (API-key session)", n)
	}
}

func TestFoldUsageMalformedBreakdownKeepsCostAndContext(t *testing.T) {
	h := newFeedHarness(t, nil)
	ctx := map[string]any{"tokens": 4000, "window": 200000, "percent": 2, "breakdown": "garbage"}
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "a", Usage: hostUsage(t, map[string]any{"context": ctx})})
	u := h.usageNow(t)
	if u.Session.CostUSD != 0.25 || u.Context == nil || u.Context.Tokens != 4000 {
		t.Fatalf("usage = %+v; want cost and context kept", u)
	}
	if u.Context.Breakdown != (ContextBreakdown{}) {
		t.Fatalf("breakdown = %+v from garbage", u.Context.Breakdown)
	}
}

func TestFoldUsageBreakdownClampsNegativeTokens(t *testing.T) {
	h := newFeedHarness(t, nil)
	ctx := map[string]any{"tokens": 1, "window": 200000, "percent": 1, "breakdown": map[string]any{
		"categories": []map[string]any{{"name": "Messages", "tokens": -50, "kind": "used"}, {"name": "System prompt", "tokens": 7, "kind": "used"}},
	}}
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "a", Usage: hostUsage(t, map[string]any{"context": ctx})})
	if got := h.usageNow(t).Context.Breakdown; got != (ContextBreakdown{System: 7}) {
		t.Fatalf("breakdown = %+v", got)
	}
}

func TestFoldUsageModelRejectsControlCharacters(t *testing.T) {
	cases := map[string]string{
		"plain":   "claude-sonnet-5-5",
		"escape":  "claude\x1b[31m-evil",
		"newline": "a\nb",
		"long":    strings.Repeat("m", 500),
	}
	want := map[string]string{
		"plain":   "claude-sonnet-5-5",
		"escape":  "",
		"newline": "",
		"long":    strings.Repeat("m", MaxActionDetail),
	}
	for name, model := range cases {
		t.Run(name, func(t *testing.T) {
			h := newFeedHarness(t, nil)
			h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "a", Usage: hostUsage(t, map[string]any{"model": model})})
			if got := h.usageNow(t).Model; got != want[name] {
				t.Fatalf("model = %q; want %q", got, want[name])
			}
		})
	}
}

func TestFoldUsageIdenticalRepeatPublishesNothing(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.EventSessionEnd, EventID: "a", Usage: hostUsage(t, nil)})
	h.send(bridge.Event{Name: bridge.EventSessionEnd, EventID: "b", Usage: hostUsage(t, nil)})
	if n := len(h.ofType(EventAgentUsage)); n != 1 {
		t.Fatalf("agent_usage events = %d; want 1 (identical repeat)", n)
	}
	p := h.ofType(EventAgentUsage)[0].Payload.(*AgentUsagePayload)
	if p.Usage.Model == "" || len(p.Usage.RateLimits) != 2 || p.Usage.Context.Breakdown == (ContextBreakdown{}) {
		t.Fatalf("payload usage = %+v", p.Usage)
	}
}

func TestBridgeAgentsUsageDoesNotAliasRateLimits(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "a", Usage: hostUsage(t, nil)})
	h.usageNow(t).RateLimits[0].PercentUsed = 99
	if got := h.usageNow(t).RateLimits[0].PercentUsed; got != 41.5 {
		t.Fatalf("caller mutation reached the feed's state: %v", got)
	}
}

func TestBridgeFeedEffort(t *testing.T) {
	h := newFeedHarness(t, nil)

	h.send(bridge.Event{Name: bridge.EventEffort, EventID: "e1", Effort: "high"})
	if got := h.usageNow(t).Effort; got != "high" {
		t.Fatalf("effort = %q", got)
	}
	if n := len(h.ofType(EventAgentUsage)); n != 1 {
		t.Fatalf("agent_usage events = %d after the first effort", n)
	}

	h.send(bridge.Event{Name: bridge.EventEffort, EventID: "e2", Effort: "high"})
	if n := len(h.ofType(EventAgentUsage)); n != 1 {
		t.Fatalf("agent_usage events = %d; a repeated effort must publish nothing", n)
	}

	h.send(bridge.Event{Name: bridge.EventEffort, EventID: "e3", Effort: "max"})
	if n := len(h.ofType(EventAgentUsage)); n != 2 || h.usageNow(t).Effort != "max" {
		t.Fatalf("effort change: events=%d usage=%+v", n, h.usageNow(t))
	}

	// A relaunch (new launch of the key) forgets it: the new process has not
	// said what effort it runs at.
	h.store.BindBridgeKey("alice-key", "alice-key-L2", "alice")
	h.send(bridge.Event{Name: bridge.ReportHello, Gen: 2, LaunchID: "alice-key-L2"})
	if got := h.usageNow(t).Effort; got != "" {
		t.Fatalf("effort = %q after a relaunch; want cleared", got)
	}
	evs := h.ofType(EventAgentUsage)
	if last := evs[len(evs)-1].Payload.(*AgentUsagePayload); last.Usage.Effort != "" {
		t.Fatalf("relaunch published stale effort %q", last.Usage.Effort)
	}
}

func TestTurnCompletePublishesUsageOnlyForObservedFieldChanges(t *testing.T) {
	limits := func(pct float64) []map[string]any {
		return []map[string]any{{"kind": "five_hour", "percentUsed": pct, "resetsAt": "2026-11-12T05:00:00Z"}}
	}
	h := newFeedHarness(t, nil)

	// First report brings a model and limits: both events.
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "a", Usage: hostUsage(t, map[string]any{"rateLimits": limits(10)})})
	if len(h.ofType(EventAgentTurnCompleted)) != 1 || len(h.ofType(EventAgentUsage)) != 1 {
		t.Fatalf("first turn: %v", eventTypes(h.pub.snapshot()))
	}

	// Cost and context move, nothing observed does: the turn event alone.
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "b", Usage: hostUsage(t, map[string]any{"rateLimits": limits(10), "cost": map[string]any{"usd": 0.5}})})
	if len(h.ofType(EventAgentTurnCompleted)) != 2 || len(h.ofType(EventAgentUsage)) != 1 {
		t.Fatalf("cost-only turn: %v", eventTypes(h.pub.snapshot()))
	}

	// New limits: both again, and the usage event carries them.
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "c", Usage: hostUsage(t, map[string]any{"rateLimits": limits(11)})})
	usage := h.ofType(EventAgentUsage)
	if len(h.ofType(EventAgentTurnCompleted)) != 3 || len(usage) != 2 {
		t.Fatalf("limits turn: %v", eventTypes(h.pub.snapshot()))
	}
	if got := usage[1].Payload.(*AgentUsagePayload).Usage.RateLimits[0].PercentUsed; got != 11 {
		t.Fatalf("published percent = %v", got)
	}

	// A new model: both.
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "d", Usage: hostUsage(t, map[string]any{"rateLimits": limits(11), "model": "claude-sonnet-5-5"})})
	if len(h.ofType(EventAgentUsage)) != 3 {
		t.Fatalf("model turn: %v", eventTypes(h.pub.snapshot()))
	}
}

func gitOf(e Event) *GitStats { return e.Payload.(*AgentUsagePayload).Usage.Git }

func TestSetGitStatsPublishesUsageWithEverythingElse(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.EventEffort, EventID: "e", Effort: "high"})
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "a", Usage: hostUsage(t, nil)})
	h.pub.reset()

	h.feed.SetGitStats(map[string]GitStats{"alice": {Added: 3, Removed: 1}})

	evs := h.ofType(EventAgentUsage)
	if len(evs) != 1 {
		t.Fatalf("agent_usage events = %d; want 1", len(evs))
	}
	got := evs[0].Payload.(*AgentUsagePayload)
	if got.Agent != "alice" || *got.Usage.Git != (GitStats{Added: 3, Removed: 1}) {
		t.Fatalf("payload = %+v", got)
	}
	if got.Usage.Effort != "high" || got.Usage.Session.CostUSD != 0.25 || got.Usage.Context == nil || len(got.Usage.RateLimits) != 2 || got.Usage.Model == "" {
		t.Fatalf("git publish lost the rest of the usage: %+v", got.Usage)
	}
	if u := h.usageNow(t); u.Git == nil || *u.Git != (GitStats{Added: 3, Removed: 1}) {
		t.Fatalf("BridgeAgents usage git = %+v", u.Git)
	}
}

func TestSetGitStatsUnchangedPublishesNothing(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "a", Usage: hostUsage(t, nil)})
	h.feed.SetGitStats(map[string]GitStats{"alice": {Added: 1}})
	h.pub.reset()

	h.feed.SetGitStats(map[string]GitStats{"alice": {Added: 1}})

	if n := len(h.pub.snapshot()); n != 0 {
		t.Fatalf("published %d events for an unchanged map", n)
	}
}

func TestSetGitStatsRemovalPublishesOnce(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "a", Usage: hostUsage(t, nil)})
	h.feed.SetGitStats(map[string]GitStats{"alice": {Added: 1}})
	h.pub.reset()

	h.feed.SetGitStats(map[string]GitStats{})
	h.feed.SetGitStats(nil)

	evs := h.ofType(EventAgentUsage)
	if len(evs) != 1 || gitOf(evs[0]) != nil {
		t.Fatalf("removal events = %d, git = %v; want one without git", len(evs), evs)
	}
	if u := h.usageNow(t); u.Git != nil {
		t.Fatalf("git still on usage: %+v", u.Git)
	}
}

func TestBridgeReportsKeepCurrentGit(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "a", Usage: hostUsage(t, nil)})
	h.feed.SetGitStats(map[string]GitStats{"alice": {Added: 5, Removed: 2}})
	h.pub.reset()

	h.send(bridge.Event{Name: bridge.EventSessionEnd, EventID: "b", Usage: hostUsage(t, map[string]any{"cost": map[string]any{"usd": 0.9}})})
	h.send(bridge.Event{Name: bridge.EventEffort, EventID: "c", Effort: "max"})

	evs := h.ofType(EventAgentUsage)
	if len(evs) != 2 {
		t.Fatalf("agent_usage events = %d", len(evs))
	}
	for i, e := range evs {
		if g := gitOf(e); g == nil || *g != (GitStats{Added: 5, Removed: 2}) {
			t.Fatalf("event %d git = %v; want the current stats", i, g)
		}
	}
}

func TestSetGitStatsWithoutBridgeUsageIsHeldNotPublished(t *testing.T) {
	h := newFeedHarness(t, nil)

	h.feed.SetGitStats(map[string]GitStats{"alice": {Added: 4}, "codex-agent": {Added: 9}})

	if n := len(h.pub.snapshot()); n != 0 {
		t.Fatalf("published %d events for agents with no bridge usage", n)
	}
	if got := h.feed.GitStats(); got["alice"] != (GitStats{Added: 4}) {
		t.Fatalf("GitStats() = %v; want the held map", got)
	}
	// Once bridge usage exists it carries what was held.
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "a", Usage: hostUsage(t, nil)})
	if u := h.usageNow(t); u.Git == nil || u.Git.Added != 4 {
		t.Fatalf("usage git = %+v", u.Git)
	}
}

func TestBridgeAgentsGitDoesNotAliasHeldMap(t *testing.T) {
	h := newFeedHarness(t, nil)
	h.send(bridge.Event{Name: bridge.EventTurnComplete, EventID: "a", Usage: hostUsage(t, nil)})
	in := map[string]GitStats{"alice": {Added: 1}}
	h.feed.SetGitStats(in)
	in["alice"] = GitStats{Added: 99}
	h.usageNow(t).Git.Added = 77
	if got := h.usageNow(t).Git.Added; got != 1 {
		t.Fatalf("held git = %d; caller mutation leaked", got)
	}
}
