package harness

// Usage is measured harness-native accounting. A nil member means the
// harness did not report that quantity; zero is therefore a real measurement.
type Usage struct {
	InputTokens  *int64
	OutputTokens *int64
	CostUSD      *float64
	Turns        *int
	ToolCalls    *int
}

func Int64(v int64) *int64       { return &v }
func Int(v int) *int             { return &v }
func Float64(v float64) *float64 { return &v }

func (u Usage) Clone() Usage {
	return Usage{InputTokens: cloneInt64(u.InputTokens), OutputTokens: cloneInt64(u.OutputTokens), CostUSD: cloneFloat64(u.CostUSD), Turns: cloneInt(u.Turns), ToolCalls: cloneInt(u.ToolCalls)}
}

func cloneInt64(v *int64) *int64 {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}
func cloneFloat64(v *float64) *float64 {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}
func cloneInt(v *int) *int {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}

// UsageAccumulator consumes complete native JSONL records. Implementations
// must be idempotent for duplicate provider IDs.
type UsageAccumulator interface {
	AddLine([]byte)
	Usage() *Usage
}

// UsageAccounter is deliberately optional so third-party harnesses retain the
// small Harness contract and simply report unknown usage.
type UsageAccounter interface{ NewUsageAccumulator() UsageAccumulator }
