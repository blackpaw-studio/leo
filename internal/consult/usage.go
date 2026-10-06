package consult

import (
	"encoding/json"
	"math"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// bridgeSessionUsage is the part of the leo-bridge mod's session usage
// ($.session.usage()) a dispatch record carries: the session's running cost.
type bridgeSessionUsage struct {
	Cost *struct {
		USD *float64 `json:"usd"`
	} `json:"cost"`
}

// ApplyBridgeUsage records a bridged interactive dispatch's session usage
// as reported with a completed turn. The cost is the claude session's
// running total, so it replaces the previous report rather than adding to
// it; token counts are not reported, so the usage is marked incomplete.
// Usage without a valid cost, or for an unknown or settled run, is ignored.
func (d *Dispatcher) ApplyBridgeUsage(id string, raw json.RawMessage) {
	var u bridgeSessionUsage
	if json.Unmarshal(raw, &u) != nil || u.Cost == nil || u.Cost.USD == nil {
		return
	}
	usd := *u.Cost.USD
	if usd < 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return
	}
	_, s, err := d.lookup(id)
	if err != nil || s == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.record.Mode != ModeInteractive || s.record.Status.Terminal() {
		return
	}
	if len(s.record.UsageInvocations) == 0 {
		d.beginUsageInvocationLocked(s)
	}
	d.applyUsageLocked(s, &harness.Usage{CostUSD: harness.Float64(usd), Incomplete: true}, false)
}

func (d *Dispatcher) beginUsageInvocationLocked(state *runState) {
	state.record.UsageInvocations = append(state.record.UsageInvocations, InvocationUsage{})
}

// applyUsageLocked replaces this invocation's provisional values with the
// accumulator snapshot. Future continuations can add a separate invocation
// snapshot without ever adding a final result to its provisional predecessor.
func (d *Dispatcher) applyUsageLocked(state *runState, usage *harness.Usage, completed bool) {
	if usage == nil {
		return
	}
	if len(state.record.UsageInvocations) == 0 {
		state.record.UsageInvocations = append(state.record.UsageInvocations, InvocationUsage{})
	}
	i := len(state.record.UsageInvocations) - 1
	state.record.UsageInvocations[i] = InvocationUsage{InputTokens: clonePtr(usage.InputTokens), OutputTokens: clonePtr(usage.OutputTokens), CostUSD: clonePtr(usage.CostUSD), UsageTurns: clonePtr(usage.Turns), ToolCalls: clonePtr(usage.ToolCalls), Incomplete: usage.Incomplete, Completed: completed}
	var bad bool
	state.record.InputTokens, bad = sumInt64(state.record.UsageInvocations, func(u InvocationUsage) *int64 { return u.InputTokens })
	state.record.UsageIncomplete = bad
	state.record.OutputTokens, bad = sumInt64(state.record.UsageInvocations, func(u InvocationUsage) *int64 { return u.OutputTokens })
	state.record.UsageIncomplete = state.record.UsageIncomplete || bad
	state.record.CostUSD, bad = sumFloat64(state.record.UsageInvocations, func(u InvocationUsage) *float64 { return u.CostUSD })
	state.record.UsageIncomplete = state.record.UsageIncomplete || bad
	state.record.UsageTurns, bad = sumInt(state.record.UsageInvocations, func(u InvocationUsage) *int { return u.UsageTurns })
	state.record.UsageIncomplete = state.record.UsageIncomplete || bad
	state.record.ToolCalls, bad = sumInt(state.record.UsageInvocations, func(u InvocationUsage) *int { return u.ToolCalls })
	state.record.UsageIncomplete = state.record.UsageIncomplete || bad
	for _, invocation := range state.record.UsageInvocations {
		state.record.UsageIncomplete = state.record.UsageIncomplete || invocation.Incomplete
	}
	d.persistRecordLocked(state)
}

func sumInt64(items []InvocationUsage, field func(InvocationUsage) *int64) (*int64, bool) {
	var total int64
	for _, item := range items {
		if v := field(item); v != nil {
			var ok bool
			total, ok = harness.AddInt64(total, *v)
			if !ok {
				return harness.Int64(math.MaxInt64), true
			}
		} else {
			return nil, false
		}
	}
	return &total, false
}
func sumFloat64(items []InvocationUsage, field func(InvocationUsage) *float64) (*float64, bool) {
	var total float64
	for _, item := range items {
		if v := field(item); v != nil {
			if *v < 0 || math.IsNaN(*v) || math.IsInf(*v, 0) || total > math.MaxFloat64-*v {
				return harness.Float64(math.MaxFloat64), true
			}
			total += *v
		} else {
			return nil, false
		}
	}
	return &total, false
}
func sumInt(items []InvocationUsage, field func(InvocationUsage) *int) (*int, bool) {
	var total int
	for _, item := range items {
		if v := field(item); v != nil {
			var ok bool
			total, ok = harness.AddInt(total, *v)
			if !ok {
				max := int(^uint(0) >> 1)
				return &max, true
			}
		} else {
			return nil, false
		}
	}
	return &total, false
}
