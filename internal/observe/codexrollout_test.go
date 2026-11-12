package observe

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// rolloutLine builds one synthetic rollout line.
func tcLine(ts, typ, payload string) []byte {
	return fmt.Appendf(nil, `{"timestamp":%q,"type":%q,"payload":%s}`, ts, typ, payload)
}

func tcCtx(ts, model, effort string) []byte {
	p := fmt.Sprintf(`{"model":%q`, model)
	if effort != "" {
		p += fmt.Sprintf(`,"effort":%q`, effort)
	}
	return tcLine(ts, "turn_context", p+"}")
}

// tcCount builds a token_count line. rate is raw JSON for rate_limits ("" omits it).
func tcCount(ts string, total, last, window int64, rate string) []byte {
	p := fmt.Sprintf(`{"type":"token_count","info":{"total_token_usage":{"total_tokens":%d},"last_token_usage":{"total_tokens":%d},"model_context_window":%d}`, total, last, window)
	if rate != "" {
		p += `,"rate_limits":` + rate
	}
	return tcLine(ts, "event_msg", p+"}")
}

func tcRate(id string, windows ...string) string {
	parts := []string{fmt.Sprintf(`"limit_id":%q`, id)}
	names := []string{"primary", "secondary"}
	for i, w := range windows {
		parts = append(parts, fmt.Sprintf(`%q:%s`, names[i], w))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func tcWin(pct float64, minutes, resets int64) string {
	return fmt.Sprintf(`{"used_percent":%v,"window_minutes":%d,"resets_at":%d}`, pct, minutes, resets)
}

func TestCodexRolloutFold(t *testing.T) {
	started := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	const at1, at2, at3 = "2026-11-12T10:00:02Z", "2026-11-12T10:05:00Z", "2026-11-12T10:10:00Z"
	reset5 := time.Unix(1794000000, 0).UTC()
	reset7 := time.Unix(1794500000, 0).UTC()

	tests := []struct {
		name    string
		started time.Time
		lines   [][]byte
		check   func(t *testing.T, u AgentUsage)
	}{
		{
			name: "model and effort come from turn_context, effort omitted when absent",
			lines: [][]byte{
				tcCtx(at1, "gpt-test-1", "high"),
				tcCtx(at2, "gpt-test-2", ""),
			},
			check: func(t *testing.T, u AgentUsage) {
				if u.Model != "gpt-test-2" || u.Effort != "" {
					t.Fatalf("model=%q effort=%q", u.Model, u.Effort)
				}
			},
		},
		{
			name:  "a model with a control character is dropped",
			lines: [][]byte{tcCtx(at1, "gpt\x1b[31m", "")},
			check: func(t *testing.T, u AgentUsage) {
				if u.Model != "" {
					t.Fatalf("model=%q", u.Model)
				}
			},
		},
		{
			name:  "context uses last_token_usage over the window, not the cumulative total",
			lines: [][]byte{tcCount(at1, 5000, 2000, 10000, "")},
			check: func(t *testing.T, u AgentUsage) {
				if u.Context == nil || u.Context.Tokens != 2000 || u.Context.Window != 10000 || u.Context.Percent != 20 {
					t.Fatalf("context=%+v", u.Context)
				}
			},
		},
		{
			name:  "a zero window omits the context",
			lines: [][]byte{tcCount(at1, 5000, 2000, 0, "")},
			check: func(t *testing.T, u AgentUsage) {
				if u.Context != nil {
					t.Fatalf("context=%+v", u.Context)
				}
			},
		},
		{
			name: "windows map by window_minutes, whichever slot holds them",
			lines: [][]byte{tcCount(at1, 1, 1, 10, tcRate("codex",
				tcWin(40, 10080, 1794500000)))},
			check: func(t *testing.T, u AgentUsage) {
				want := []RateLimit{{Kind: RateLimitSevenDay, PercentUsed: 40, ResetsAt: reset7}}
				if !equalRateLimits(u.RateLimits, want) {
					t.Fatalf("limits=%+v", u.RateLimits)
				}
			},
		},
		{
			name: "both windows, reset in epoch seconds as UTC, unknown window dropped",
			lines: [][]byte{tcCount(at1, 1, 1, 10, tcRate("codex",
				tcWin(12.5, 300, 1794000000), tcWin(40, 10080, 1794500000)))},
			check: func(t *testing.T, u AgentUsage) {
				want := []RateLimit{
					{Kind: RateLimitFiveHour, PercentUsed: 12.5, ResetsAt: reset5},
					{Kind: RateLimitSevenDay, PercentUsed: 40, ResetsAt: reset7},
				}
				if !equalRateLimits(u.RateLimits, want) {
					t.Fatalf("limits=%+v", u.RateLimits)
				}
			},
		},
		{
			name:  "a 60-minute window is dropped",
			lines: [][]byte{tcCount(at1, 1, 1, 10, tcRate("codex", tcWin(5, 60, 1794000000)))},
			check: func(t *testing.T, u AgentUsage) {
				if len(u.RateLimits) != 0 {
					t.Fatalf("limits=%+v", u.RateLimits)
				}
			},
		},
		{
			name:  "a percent outside 0-1000 is dropped",
			lines: [][]byte{tcCount(at1, 1, 1, 10, tcRate("codex", tcWin(1001, 300, 1794000000), tcWin(-1, 10080, 1794500000)))},
			check: func(t *testing.T, u AgentUsage) {
				if len(u.RateLimits) != 0 {
					t.Fatalf("limits=%+v", u.RateLimits)
				}
			},
		},
		{
			name:  "another limit_id is ignored",
			lines: [][]byte{tcCount(at1, 1, 1, 10, tcRate("codex_other", tcWin(5, 300, 1794000000)))},
			check: func(t *testing.T, u AgentUsage) {
				if len(u.RateLimits) != 0 {
					t.Fatalf("limits=%+v", u.RateLimits)
				}
			},
		},
		{
			name: "null rate_limits keeps the previous values",
			lines: [][]byte{
				tcCount(at1, 1, 1, 10, tcRate("codex", tcWin(12.5, 300, 1794000000))),
				tcCount(at2, 2, 1, 10, "null"),
				tcCount(at3, 3, 1, 10, ""),
			},
			check: func(t *testing.T, u AgentUsage) {
				want := []RateLimit{{Kind: RateLimitFiveHour, PercentUsed: 12.5, ResetsAt: reset5}}
				if !equalRateLimits(u.RateLimits, want) {
					t.Fatalf("limits=%+v", u.RateLimits)
				}
			},
		},
		{
			name: "tokens are the sum of total deltas; the codex cost is unknown",
			lines: [][]byte{
				tcCount(at1, 1000, 1000, 10000, ""),
				tcCount(at2, 1000, 1000, 10000, ""), // repeated event: no delta
				tcCount(at3, 2500, 1500, 10000, ""),
			},
			check: func(t *testing.T, u AgentUsage) {
				if u.Session.Tokens != 2500 || !u.Session.CostUnknown || !u.Incarnation.CostUnknown {
					t.Fatalf("session=%+v incarnation=%+v", u.Session, u.Incarnation)
				}
			},
		},
		{
			name: "a total that goes backwards counts from zero",
			lines: [][]byte{
				tcCount(at1, 1000, 1000, 10000, ""),
				tcCount(at2, 300, 300, 10000, ""),
			},
			check: func(t *testing.T, u AgentUsage) {
				if u.Session.Tokens != 1300 {
					t.Fatalf("session=%d", u.Session.Tokens)
				}
			},
		},
		{
			name:    "incarnation counts only deltas at or after the agent started",
			started: started.Add(time.Minute),
			lines: [][]byte{
				tcCount(at1, 1000, 1000, 10000, ""), // before the start
				tcCount(at2, 1600, 600, 10000, ""),  // after
			},
			check: func(t *testing.T, u AgentUsage) {
				if u.Session.Tokens != 1600 || u.Incarnation.Tokens != 600 {
					t.Fatalf("session=%d incarnation=%d", u.Session.Tokens, u.Incarnation.Tokens)
				}
			},
		},
		{
			name: "null info updates rate limits only",
			lines: [][]byte{
				tcCount(at1, 1000, 1000, 10000, ""),
				tcLine(at2, "event_msg", `{"type":"token_count","info":null,"rate_limits":`+tcRate("codex", tcWin(7, 300, 1794000000))+`}`),
			},
			check: func(t *testing.T, u AgentUsage) {
				if u.Session.Tokens != 1000 || u.Context == nil || u.Context.Tokens != 1000 || len(u.RateLimits) != 1 {
					t.Fatalf("usage=%+v", u)
				}
			},
		},
		{
			name: "garbage and unrelated lines are ignored",
			lines: [][]byte{
				[]byte("not json"), []byte(`{"type":"event_msg","payload":`), []byte(`[]`), nil,
				tcLine(at1, "response_item", `{"type":"message"}`),
				tcLine(at1, "event_msg", `{"type":"token_count","info":"x"}`),
				tcCount(at2, 10, 10, 100, ""),
			},
			check: func(t *testing.T, u AgentUsage) {
				if u.Session.Tokens != 10 {
					t.Fatalf("session=%d", u.Session.Tokens)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newCodexRolloutFold(tc.started)
			for _, l := range tc.lines {
				f.AddLine(l)
			}
			tc.check(t, f.Usage("sid"))
		})
	}

	t.Run("synthetic fixture file", func(t *testing.T) {
		raw, err := os.ReadFile("testdata/codex-rollout.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		f := newCodexRolloutFold(started)
		for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			f.AddLine([]byte(l))
		}
		u := f.Usage("00000000-0000-4000-8000-000000000001")
		if u.SessionID != "00000000-0000-4000-8000-000000000001" || u.Model != "gpt-test-1" || u.Effort != "high" ||
			u.Session.Tokens != 2500 || u.Incarnation.Tokens != 2500 ||
			u.Context == nil || u.Context.Tokens != 1700 || u.Context.Percent != 1.7 ||
			len(u.RateLimits) != 2 {
			t.Fatalf("usage=%+v context=%+v", u, u.Context)
		}
	})

	t.Run("reset clears the session but keeps rate limits", func(t *testing.T) {
		f := newCodexRolloutFold(started)
		f.AddLine(tcCtx(at1, "gpt-test-1", "high"))
		f.AddLine(tcCount(at1, 1000, 1000, 10000, tcRate("codex", tcWin(12.5, 300, 1794000000))))
		f.Reset()
		u := f.Usage("sid")
		if u.Model != "" || u.Effort != "" || u.Context != nil || u.Session.Tokens != 0 || u.Incarnation.Tokens != 0 || len(u.RateLimits) != 1 {
			t.Fatalf("usage=%+v", u)
		}
	})
}
