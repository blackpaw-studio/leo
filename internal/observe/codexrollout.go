package observe

import (
	"bytes"
	"encoding/json"
	"math"
	"time"
)

// Codex rate-limit windows by length. Codex reports them in primary/secondary
// slots that don't say which window they hold (primary can carry the weekly
// window while secondary is null), so the length decides.
const (
	codexFiveHourMinutes = 300
	codexSevenDayMinutes = 10080
	// codexLimitID is the rate-limit family the fold reports; other families
	// (per-model limits) are not the account's session limit.
	codexLimitID = "codex"
)

// codexRolloutFold folds a codex rollout's lines into an AgentUsage. It is
// pure: feed it lines in file order and read Usage. The zero value is not
// usable; build one with newCodexRolloutFold.
type codexRolloutFold struct {
	startedAt time.Time

	model, effort string
	context       *ContextUsage
	// prevTotal is the last cumulative total_token_usage.total_tokens seen.
	prevTotal   int64
	session     int64
	incarnation int64
	// rateLimits is account-scoped, so it survives Reset.
	rateLimits []RateLimit
}

// newCodexRolloutFold starts a fold for an agent whose process started at
// startedAt: incarnation tokens count only lines stamped at or after it (zero
// counts everything).
func newCodexRolloutFold(startedAt time.Time) *codexRolloutFold {
	return &codexRolloutFold{startedAt: startedAt}
}

// Reset starts the per-session state over for a new rollout file: tokens,
// context, model and effort go until the next lines restate them. Rate limits
// stay, being the account's.
func (f *codexRolloutFold) Reset() {
	f.model, f.effort, f.context = "", "", nil
	f.prevTotal, f.session, f.incarnation = 0, 0, 0
}

type codexRolloutLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexRolloutPayload struct {
	Type       string          `json:"type"`
	Model      string          `json:"model"`
	Effort     string          `json:"effort"`
	Info       json.RawMessage `json:"info"`
	RateLimits json.RawMessage `json:"rate_limits"`
}

type codexTokenInfo struct {
	Total *struct {
		TotalTokens *int64 `json:"total_tokens"`
	} `json:"total_token_usage"`
	Last *struct {
		TotalTokens *int64 `json:"total_tokens"`
	} `json:"last_token_usage"`
	Window *int64 `json:"model_context_window"`
}

type codexRateLimits struct {
	LimitID   string       `json:"limit_id"`
	Primary   *codexWindow `json:"primary"`
	Secondary *codexWindow `json:"secondary"`
}

type codexWindow struct {
	UsedPercent *float64 `json:"used_percent"`
	Minutes     int64    `json:"window_minutes"`
	// ResetsAt is epoch seconds.
	ResetsAt int64 `json:"resets_at"`
}

// AddLine folds one rollout line in. Lines it cannot read, and lines of any
// other kind, are ignored.
func (f *codexRolloutFold) AddLine(line []byte) {
	var l codexRolloutLine
	if json.Unmarshal(line, &l) != nil {
		return
	}
	var p codexRolloutPayload
	if json.Unmarshal(l.Payload, &p) != nil {
		return
	}
	switch {
	case l.Type == "turn_context":
		f.model, f.effort = cleanModel(p.Model), cleanModel(p.Effort)
	case l.Type == "event_msg" && p.Type == "token_count":
		f.addTokenCount(l.Timestamp, p)
	}
}

func (f *codexRolloutFold) addTokenCount(ts string, p codexRolloutPayload) {
	if !isJSONNull(p.Info) {
		var info codexTokenInfo
		if json.Unmarshal(p.Info, &info) == nil {
			f.addInfo(ts, info)
		}
	}
	if !isJSONNull(p.RateLimits) {
		var rl codexRateLimits
		if json.Unmarshal(p.RateLimits, &rl) == nil {
			f.addRateLimits(rl)
		}
	}
}

func (f *codexRolloutFold) addInfo(ts string, info codexTokenInfo) {
	if info.Total != nil && info.Total.TotalTokens != nil && *info.Total.TotalTokens >= 0 {
		total := *info.Total.TotalTokens
		delta := total - f.prevTotal
		if delta < 0 {
			// Codex restarted its totals (a resumed session): count from zero.
			delta = total
		}
		f.prevTotal = total
		f.session = addSaturating(f.session, delta)
		if f.countsForIncarnation(ts) {
			f.incarnation = addSaturating(f.incarnation, delta)
		}
	}
	if info.Window == nil || info.Last == nil || info.Last.TotalTokens == nil {
		return
	}
	tokens, window := *info.Last.TotalTokens, *info.Window
	if window <= 0 || tokens < 0 {
		f.context = nil
		return
	}
	// The codex TUI's own figure: the last request's total over the window,
	// no baseline subtracted.
	f.context = &ContextUsage{Tokens: tokens, Window: window, Percent: float64(tokens) * 100 / float64(window)}
}

func (f *codexRolloutFold) countsForIncarnation(ts string) bool {
	if f.startedAt.IsZero() {
		return true
	}
	at, err := time.Parse(time.RFC3339, ts)
	return err == nil && !at.Before(f.startedAt)
}

// addRateLimits replaces the limits with the report's, but only when it is the
// account's family and carries at least one usable window: a report that has
// none says nothing.
func (f *codexRolloutFold) addRateLimits(rl codexRateLimits) {
	if rl.LimitID != "" && rl.LimitID != codexLimitID {
		return
	}
	var out []RateLimit
	for _, w := range []*codexWindow{rl.Primary, rl.Secondary} {
		if limit, ok := w.limit(); ok {
			out = append(out, limit)
		}
	}
	if len(out) == 0 {
		return
	}
	// Five-hour first, whichever slot held it.
	if len(out) == 2 && out[0].Kind == RateLimitSevenDay {
		out[0], out[1] = out[1], out[0]
	}
	f.rateLimits = out
}

func (w *codexWindow) limit() (RateLimit, bool) {
	if w == nil || w.UsedPercent == nil {
		return RateLimit{}, false
	}
	var kind RateLimitKind
	switch w.Minutes {
	case codexFiveHourMinutes:
		kind = RateLimitFiveHour
	case codexSevenDayMinutes:
		kind = RateLimitSevenDay
	default:
		return RateLimit{}, false
	}
	pct := *w.UsedPercent
	if math.IsNaN(pct) || pct < 0 || pct > maxRateLimitPercent {
		return RateLimit{}, false
	}
	out := RateLimit{Kind: kind, PercentUsed: pct}
	if w.ResetsAt > 0 && w.ResetsAt < 1e12 {
		out.ResetsAt = time.Unix(w.ResetsAt, 0).UTC()
	}
	return out, true
}

// Usage is what the fold has read so far, for the agent's rollout sessionID.
// Codex reports no cost, so both totals are marked CostUnknown.
func (f *codexRolloutFold) Usage(sessionID string) AgentUsage {
	return AgentUsage{
		SessionID:   sessionID,
		Session:     UsageTotals{Tokens: f.session, CostUnknown: true},
		Incarnation: UsageTotals{Tokens: f.incarnation, CostUnknown: true},
		Context:     cloneOf(f.context),
		Model:       f.model,
		Effort:      f.effort,
		RateLimits:  append([]RateLimit(nil), f.rateLimits...),
	}
}

func isJSONNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}
