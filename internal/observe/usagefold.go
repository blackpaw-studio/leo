package observe

import (
	"encoding/json"
	"math"
	"slices"
	"strings"
	"time"
)

// maxRateLimitPercent bounds a reported limit percentage; hosts can report a
// little over 100 near the edge, anything wildly above is malformed.
const maxRateLimitPercent = 1000

// hostBreakdown is the host's /context summary
// ($.session.usage({breakdown:'summary'}).context.breakdown).
type hostBreakdown struct {
	Categories []struct {
		Name   string `json:"name"`
		Tokens int64  `json:"tokens"`
		Kind   string `json:"kind"`
	} `json:"categories"`
	AutoCompactThreshold *float64 `json:"autoCompactThreshold"`
	IsAutoCompactEnabled bool     `json:"isAutoCompactEnabled"`
}

// hostRateLimit is one entry of the host's rateLimits. ResetsAt is an RFC3339
// string, or a number of epoch milliseconds.
type hostRateLimit struct {
	Kind        string          `json:"kind"`
	PercentUsed *float64        `json:"percentUsed"`
	ResetsAt    json.RawMessage `json:"resetsAt"`
}

// foldBreakdown maps the used rows of the host's summary onto the five
// categories: any name it does not know counts as tools, so the categories
// still sum to what /context shows.
func foldBreakdown(b hostBreakdown) ContextBreakdown {
	var out ContextBreakdown
	for _, row := range b.Categories {
		if row.Kind != "used" {
			continue
		}
		n := max(row.Tokens, 0)
		switch row.Name {
		case "System prompt":
			out.System = addSaturating(out.System, n)
		case "MCP tools":
			out.MCP = addSaturating(out.MCP, n)
		case "Memory files":
			out.Memory = addSaturating(out.Memory, n)
		case "Messages":
			out.Messages = addSaturating(out.Messages, n)
		default: // System tools, Skills, Custom agents, Slash commands, unknown
			out.Tools = addSaturating(out.Tools, n)
		}
	}
	return out
}

// compactAt is the auto-compact threshold as a fraction of window, or 0 (not
// reported) when auto-compact is off, the threshold is missing or nonsense, or
// the window is unknown.
func compactAt(b hostBreakdown, window int64) float64 {
	if !b.IsAutoCompactEnabled || b.AutoCompactThreshold == nil || window <= 0 {
		return 0
	}
	frac := *b.AutoCompactThreshold / float64(window)
	if math.IsNaN(frac) || frac <= 0 || frac > 1 {
		return 0
	}
	return frac
}

// foldRateLimits keeps the entries it can represent: a known kind, a valid
// percentage and a parseable reset time. The rest are dropped one by one.
func foldRateLimits(raw []json.RawMessage) []RateLimit {
	var out []RateLimit
	for _, r := range raw {
		var l hostRateLimit
		if json.Unmarshal(r, &l) != nil || l.PercentUsed == nil {
			continue
		}
		kind := RateLimitKind(l.Kind)
		if kind != RateLimitFiveHour && kind != RateLimitSevenDay {
			continue
		}
		if p := *l.PercentUsed; math.IsNaN(p) || p < 0 || p > maxRateLimitPercent {
			continue
		}
		at, ok := parseResetsAt(l.ResetsAt)
		if !ok {
			continue
		}
		out = append(out, RateLimit{Kind: kind, PercentUsed: *l.PercentUsed, ResetsAt: at})
	}
	return out
}

// parseResetsAt reads an RFC3339 string or epoch milliseconds, as UTC.
func parseResetsAt(raw json.RawMessage) (time.Time, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		t, err := time.Parse(time.RFC3339, s)
		return t.UTC(), err == nil
	}
	var ms float64
	if json.Unmarshal(raw, &ms) == nil && ms > 0 && ms < 1e15 {
		return time.UnixMilli(int64(ms)).UTC(), true
	}
	return time.Time{}, false
}

// cleanModel returns the harness-reported model id, or "" for one carrying a
// control character (an escape sequence is never a model id).
func cleanModel(s string) string {
	if strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return ""
	}
	return ClampDetail(s)
}

func equalRateLimits(a, b []RateLimit) bool {
	return slices.EqualFunc(a, b, func(x, y RateLimit) bool {
		return x.Kind == y.Kind && x.PercentUsed == y.PercentUsed && x.ResetsAt.Equal(y.ResetsAt)
	})
}
