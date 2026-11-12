package observe

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAgentUsageJSON(t *testing.T) {
	reset := time.Date(2026, 11, 12, 5, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		in   AgentUsage
		want string
	}{
		{
			name: "legacy fields only omit every new key",
			in:   AgentUsage{SessionID: "s", Context: &ContextUsage{Tokens: 1, Window: 2, Percent: 50}},
			want: `{"session_id":"s","session":{"tokens":0,"cost_usd":0},"incarnation":{"tokens":0,"cost_usd":0},"context":{"tokens":1,"window":2,"percent":50}}`,
		},
		{
			name: "every new field",
			in: AgentUsage{
				SessionID: "s",
				Model:     "claude-opus-5-5",
				Effort:    "high",
				Context: &ContextUsage{
					Tokens: 1, Window: 2, Percent: 50,
					Breakdown: ContextBreakdown{System: 1, Tools: 2, MCP: 3, Memory: 4, Messages: 5},
					CompactAt: 0.8,
				},
				RateLimits: []RateLimit{{Kind: RateLimitFiveHour, PercentUsed: 12.5, ResetsAt: reset}},
				Git:        &GitStats{Added: 7, Removed: 3},
			},
			want: `{"session_id":"s","session":{"tokens":0,"cost_usd":0},"incarnation":{"tokens":0,"cost_usd":0},"context":{"tokens":1,"window":2,"percent":50,"breakdown":{"system":1,"tools":2,"mcp":3,"memory":4,"messages":5},"compact_at":0.8},"model":"claude-opus-5-5","effort":"high","rate_limits":[{"kind":"five_hour","percent_used":12.5,"resets_at":"2026-11-12T05:00:00Z"}],"git":{"added":7,"removed":3}}`,
		},
		{
			name: "rate limit without a reset time omits resets_at",
			in:   AgentUsage{SessionID: "s", RateLimits: []RateLimit{{Kind: RateLimitSevenDay, PercentUsed: 9}}},
			want: `{"session_id":"s","session":{"tokens":0,"cost_usd":0},"incarnation":{"tokens":0,"cost_usd":0},"rate_limits":[{"kind":"seven_day","percent_used":9}]}`,
		},
		{
			name: "zero git is present, nil git is absent",
			in:   AgentUsage{SessionID: "s", Git: &GitStats{}},
			want: `{"session_id":"s","session":{"tokens":0,"cost_usd":0},"incarnation":{"tokens":0,"cost_usd":0},"git":{"added":0,"removed":0}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("json =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestUsageTotalsJSON(t *testing.T) {
	tests := []struct {
		name string
		in   UsageTotals
		want string
	}{
		{"zero cost is still emitted", UsageTotals{Tokens: 5}, `{"tokens":5,"cost_usd":0}`},
		{"a claude cost is emitted", UsageTotals{Tokens: 5, CostUSD: 1.25}, `{"tokens":5,"cost_usd":1.25}`},
		{"unknown cost is omitted", UsageTotals{Tokens: 5, CostUnknown: true}, `{"tokens":5}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("json = %s, want %s", got, tc.want)
			}
		})
	}

	t.Run("a claude payload round-trips byte-identically", func(t *testing.T) {
		in := `{"session_id":"s","session":{"tokens":7,"cost_usd":0.5},"incarnation":{"tokens":3,"cost_usd":0}}`
		var u AgentUsage
		if err := json.Unmarshal([]byte(in), &u); err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(u)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != in {
			t.Fatalf("round trip = %s, want %s", out, in)
		}
	})

	t.Run("stays comparable", func(t *testing.T) {
		if (UsageTotals{Tokens: 1, CostUnknown: true}) == (UsageTotals{Tokens: 1}) {
			t.Fatal("CostUnknown must take part in equality")
		}
	})
}
