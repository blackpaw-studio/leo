package codex

import (
	"encoding/json"

	"github.com/blackpaw-studio/leo/internal/harness"
)

type usageAccumulator struct {
	tools               harness.UsageIDs
	input, output       int64
	hasInput, hasOutput bool
	turns               int
	incomplete          bool
}

func (Codex) NewUsageAccumulator() harness.UsageAccumulator {
	return &usageAccumulator{tools: harness.NewUsageIDs()}
}
func (a *usageAccumulator) AddLine(line []byte) {
	var shape struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(line, &shape) != nil {
		return
	}
	var raw struct {
		Type  string                    `json:"type"`
		Item  struct{ ID, Type string } `json:"item"`
		Usage struct {
			Input  *int64 `json:"input_tokens"`
			Output *int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(line, &raw) != nil {
		if shape.Type == "turn.completed" {
			a.incomplete = true
		}
		return
	}
	switch raw.Type {
	case "turn.started":
		if n, ok := harness.AddInt(a.turns, 1); ok {
			a.turns = n
		} else {
			a.incomplete = true
		}
	case "turn.completed":
		nextInput, inputOK := addOptional(a.input, raw.Usage.Input)
		nextOutput, outputOK := addOptional(a.output, raw.Usage.Output)
		if !inputOK || !outputOK {
			a.incomplete = true
			break
		}
		if raw.Usage.Input != nil {
			a.input, a.hasInput = nextInput, true
		}
		if raw.Usage.Output != nil {
			a.output, a.hasOutput = nextOutput, true
		}
	case "item.started", "item.completed":
		if isTool(raw.Item.Type) && raw.Item.ID != "" {
			a.tools.Add(raw.Item.ID)
		}
	}
	if a.tools.Overflow {
		a.incomplete = true
	}
}

func addOptional(total int64, value *int64) (int64, bool) {
	if value == nil {
		return total, true
	}
	return harness.AddInt64(total, *value)
}
func isTool(t string) bool {
	switch t {
	case "command_execution", "mcp_tool_call", "web_search", "file_change":
		return true
	}
	return false
}
func (a *usageAccumulator) Usage() *harness.Usage {
	if !a.hasInput && !a.hasOutput && a.turns == 0 && a.tools.Len() == 0 && !a.incomplete {
		return nil
	}
	u := harness.Usage{}
	if a.hasInput {
		u.InputTokens = harness.Int64(a.input)
	}
	if a.hasOutput {
		u.OutputTokens = harness.Int64(a.output)
	}
	if a.turns > 0 {
		u.Turns = harness.Int(a.turns)
	}
	if a.turns > 0 || a.tools.Len() > 0 {
		u.ToolCalls = harness.Int(a.tools.Len())
	}
	u.Incomplete = a.incomplete
	return &u
}
