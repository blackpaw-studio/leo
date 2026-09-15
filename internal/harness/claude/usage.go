package claude

import (
	"encoding/json"

	"github.com/blackpaw-studio/leo/internal/harness"
)

type usageAccumulator struct {
	messages, tools map[string]bool
	usage           harness.Usage
	hasAssistant    bool
}

func (Claude) NewUsageAccumulator() harness.UsageAccumulator {
	return &usageAccumulator{messages: map[string]bool{}, tools: map[string]bool{}}
}

func (a *usageAccumulator) AddLine(line []byte) {
	var raw struct {
		Type    string `json:"type"`
		Message struct {
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
	if raw.Type == "assistant" {
		for _, block := range raw.Message.Content {
			if block.Type == "tool_use" && block.ID != "" {
				a.tools[block.ID] = true
			}
		}
	}
	if raw.Type == "assistant" && !a.messages[raw.Message.ID] {
		// Some old streams omit message IDs. Such messages cannot be safely
		// deduplicated, but still provide the only provisional accounting.
		if raw.Message.ID != "" {
			a.messages[raw.Message.ID] = true
		}
		a.hasAssistant = true
		if in := raw.Message.Usage.inputTotal(); in != nil {
			if a.usage.InputTokens == nil {
				a.usage.InputTokens = harness.Int64(0)
			}
			*a.usage.InputTokens += *in
		}
	}
	if raw.Type == "result" {
		if in := raw.Usage.inputTotal(); in != nil {
			a.usage.InputTokens = in
		}
		if raw.Usage.Output != nil {
			a.usage.OutputTokens = raw.Usage.Output
		}
		if raw.TotalCostUSD != nil {
			a.usage.CostUSD = raw.TotalCostUSD
		}
		if raw.NumTurns != nil {
			a.usage.Turns = raw.NumTurns
		}
	}
	if a.hasAssistant {
		a.usage.ToolCalls = harness.Int(len(a.tools))
	}
}

func (a *usageAccumulator) Usage() *harness.Usage {
	u := a.usage.Clone()
	if u.InputTokens == nil && u.OutputTokens == nil && u.CostUSD == nil && u.Turns == nil && u.ToolCalls == nil {
		return nil
	}
	return &u
}

type tokenUsage struct {
	Input      *int64 `json:"input_tokens"`
	Output     *int64 `json:"output_tokens"`
	CacheRead  *int64 `json:"cache_read_input_tokens"`
	CacheWrite *int64 `json:"cache_creation_input_tokens"`
}

func (u tokenUsage) inputTotal() *int64 {
	if u.Input == nil && u.CacheRead == nil && u.CacheWrite == nil {
		return nil
	}
	var n int64
	if u.Input != nil {
		n += *u.Input
	}
	if u.CacheRead != nil {
		n += *u.CacheRead
	}
	if u.CacheWrite != nil {
		n += *u.CacheWrite
	}
	return harness.Int64(n)
}
