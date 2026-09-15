package consult

import "github.com/blackpaw-studio/leo/internal/harness"

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
	state.record.UsageInvocations[i] = InvocationUsage{InputTokens: clonePtr(usage.InputTokens), OutputTokens: clonePtr(usage.OutputTokens), CostUSD: clonePtr(usage.CostUSD), UsageTurns: clonePtr(usage.Turns), ToolCalls: clonePtr(usage.ToolCalls), Completed: completed}
	state.record.InputTokens = sumInt64(state.record.UsageInvocations, func(u InvocationUsage) *int64 { return u.InputTokens })
	state.record.OutputTokens = sumInt64(state.record.UsageInvocations, func(u InvocationUsage) *int64 { return u.OutputTokens })
	state.record.CostUSD = sumFloat64(state.record.UsageInvocations, func(u InvocationUsage) *float64 { return u.CostUSD })
	state.record.UsageTurns = sumInt(state.record.UsageInvocations, func(u InvocationUsage) *int { return u.UsageTurns })
	state.record.ToolCalls = sumInt(state.record.UsageInvocations, func(u InvocationUsage) *int { return u.ToolCalls })
	d.persistRecordLocked(state)
}

func sumInt64(items []InvocationUsage, field func(InvocationUsage) *int64) *int64 {
	var total int64
	for _, item := range items {
		if v := field(item); v != nil {
			total += *v
		} else {
			return nil
		}
	}
	return &total
}
func sumFloat64(items []InvocationUsage, field func(InvocationUsage) *float64) *float64 {
	var total float64
	for _, item := range items {
		if v := field(item); v != nil {
			total += *v
		} else {
			return nil
		}
	}
	return &total
}
func sumInt(items []InvocationUsage, field func(InvocationUsage) *int) *int {
	var total int
	for _, item := range items {
		if v := field(item); v != nil {
			total += *v
		} else {
			return nil
		}
	}
	return &total
}
