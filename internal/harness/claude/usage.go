package claude

import (
	"encoding/json"
	"math"

	"github.com/blackpaw-studio/leo/internal/harness"
)

type usageAccumulator struct {
	messages, tools harness.UsageIDs
	usage           harness.Usage
	hasAssistant    bool
}

func (Claude) NewUsageAccumulator() harness.UsageAccumulator {
	return &usageAccumulator{messages: harness.NewUsageIDs(), tools: harness.NewUsageIDs()}
}

func (a *usageAccumulator) AddLine(line []byte) {
	var raw struct {
		Type            string `json:"type"`
		ParentToolUseID string `json:"parent_tool_use_id"`
		Message         struct {
			ID      string                      `json:"id"`
			Usage   tokenUsage                  `json:"usage"`
			Content []struct{ Type, ID string } `json:"content"`
		} `json:"message"`
		Usage        tokenUsage `json:"usage"`
		TotalCostUSD *float64   `json:"total_cost_usd"`
		NumTurns     *int       `json:"num_turns"`
	}
	if json.Unmarshal(line, &raw) != nil {
		return
	}
	if raw.Type == "assistant" && raw.ParentToolUseID == "" {
		for _, block := range raw.Message.Content {
			if block.Type == "tool_use" && block.ID != "" {
				a.tools.Add(block.ID)
			}
		}
	}
	if raw.Type == "assistant" && raw.ParentToolUseID == "" && (raw.Message.ID == "" || a.messages.Add(raw.Message.ID)) {
		// Some old streams omit message IDs. Such messages cannot be safely
		// deduplicated, but still provide the only provisional accounting.
		a.hasAssistant = true
		if !raw.Message.Usage.valid() {
			a.usage.Incomplete = true
		} else if in, ok := raw.Message.Usage.inputTotal(); ok && in != nil {
			if a.usage.InputTokens == nil {
				a.usage.InputTokens = harness.Int64(0)
			}
			if sum, ok := harness.AddInt64(*a.usage.InputTokens, *in); ok {
				*a.usage.InputTokens = sum
			} else {
				a.usage.Incomplete = true
			}
		} else if !ok {
			a.usage.Incomplete = true
		}
	}
	if raw.Type == "result" {
		if !raw.Usage.valid() {
			a.usage.Incomplete = true
		} else {
			if in, ok := raw.Usage.inputTotal(); ok && in != nil {
				a.usage.InputTokens = in
			} else if !ok {
				a.usage.Incomplete = true
			}
			if raw.Usage.Output != nil {
				a.usage.OutputTokens = raw.Usage.Output
			}
		}
		if raw.TotalCostUSD != nil && *raw.TotalCostUSD >= 0 && !math.IsNaN(*raw.TotalCostUSD) && !math.IsInf(*raw.TotalCostUSD, 0) {
			a.usage.CostUSD = raw.TotalCostUSD
		} else if raw.TotalCostUSD != nil {
			a.usage.Incomplete = true
		}
		if raw.NumTurns != nil && *raw.NumTurns >= 0 {
			a.usage.Turns = raw.NumTurns
		} else if raw.NumTurns != nil {
			a.usage.Incomplete = true
		}
	}
	if a.messages.Overflow || a.tools.Overflow {
		a.usage.Incomplete = true
	}
	if a.hasAssistant {
		a.usage.ToolCalls = harness.Int(a.tools.Len())
	}
}

func (a *usageAccumulator) Usage() *harness.Usage {
	u := a.usage.Clone()
	if u.InputTokens == nil && u.OutputTokens == nil && u.CostUSD == nil && u.Turns == nil && u.ToolCalls == nil && !u.Incomplete {
		return nil
	}
	return &u
}

func (u tokenUsage) valid() bool {
	for _, value := range []*int64{u.Input, u.Output, u.CacheRead, u.CacheWrite} {
		if value != nil && *value < 0 {
			return false
		}
	}
	_, ok := u.inputTotal()
	return ok
}

type tokenUsage struct {
	Input      *int64 `json:"input_tokens"`
	Output     *int64 `json:"output_tokens"`
	CacheRead  *int64 `json:"cache_read_input_tokens"`
	CacheWrite *int64 `json:"cache_creation_input_tokens"`
}

func (u tokenUsage) inputTotal() (*int64, bool) {
	if u.Input == nil && u.CacheRead == nil && u.CacheWrite == nil {
		return nil, true
	}
	var n int64
	if u.Input != nil {
		var ok bool
		n, ok = harness.AddInt64(n, *u.Input)
		if !ok {
			return nil, false
		}
	}
	if u.CacheRead != nil {
		var ok bool
		n, ok = harness.AddInt64(n, *u.CacheRead)
		if !ok {
			return nil, false
		}
	}
	if u.CacheWrite != nil {
		var ok bool
		n, ok = harness.AddInt64(n, *u.CacheWrite)
		if !ok {
			return nil, false
		}
	}
	return harness.Int64(n), true
}
