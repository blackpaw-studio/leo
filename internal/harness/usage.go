package harness

import "math"

// Usage is measured harness-native accounting. A nil member means the
// harness did not report that quantity; zero is therefore a real measurement.
type Usage struct {
	InputTokens  *int64
	OutputTokens *int64
	CostUSD      *float64
	Turns        *int
	ToolCalls    *int
	Incomplete   bool
}

func Int64(v int64) *int64       { return &v }
func Int(v int) *int             { return &v }
func Float64(v float64) *float64 { return &v }

func (u Usage) Clone() Usage {
	return Usage{InputTokens: cloneInt64(u.InputTokens), OutputTokens: cloneInt64(u.OutputTokens), CostUSD: cloneFloat64(u.CostUSD), Turns: cloneInt(u.Turns), ToolCalls: cloneInt(u.ToolCalls), Incomplete: u.Incomplete}
}

const MaxUsageIDs = 10_000

type UsageIDs struct {
	Seen     map[string]struct{}
	Overflow bool
}

func NewUsageIDs() UsageIDs { return UsageIDs{Seen: make(map[string]struct{})} }
func (s *UsageIDs) Add(id string) bool {
	if id == "" {
		return false
	}
	if _, ok := s.Seen[id]; ok {
		return false
	}
	if len(s.Seen) >= MaxUsageIDs {
		s.Overflow = true
		return false
	}
	s.Seen[id] = struct{}{}
	return true
}
func (s *UsageIDs) Len() int { return len(s.Seen) }

func AddInt64(total, value int64) (int64, bool) {
	if value < 0 || total < 0 || value > math.MaxInt64-total {
		return total, false
	}
	return total + value, true
}
func AddInt(total, value int) (int, bool) {
	if value < 0 || total < 0 || value > int(^uint(0)>>1)-total {
		return total, false
	}
	return total + value, true
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
