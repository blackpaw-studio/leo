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
		{"kind": "seven_day", "percentUsed": 5, "resetsAt": "not a time"},            // unparseable reset
		{"kind": "seven_day", "percentUsed": 5},                                      // no reset
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
