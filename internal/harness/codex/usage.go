package codex

import (
	"encoding/json"

	"github.com/blackpaw-studio/leo/internal/harness"
)

type usageAccumulator struct {
	tools               map[string]bool
	input, output       int64
	hasInput, hasOutput bool
	turns               int
}

func (Codex) NewUsageAccumulator() harness.UsageAccumulator {
	return &usageAccumulator{tools: map[string]bool{}}
}
func (a *usageAccumulator) AddLine(line []byte) {
	var raw struct {
		Type  string                    `json:"type"`
		Item  struct{ ID, Type string } `json:"item"`
		Usage struct {
			Input  *int64 `json:"input_tokens"`
			Output *int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(line, &raw) != nil {
		return
	}
	switch raw.Type {
	case "turn.started":
		a.turns++
	case "turn.completed":
		if raw.Usage.Input != nil {
			a.input += *raw.Usage.Input
			a.hasInput = true
		}
		if raw.Usage.Output != nil {
			a.output += *raw.Usage.Output
			a.hasOutput = true
		}
	case "item.started", "item.completed":
		if isTool(raw.Item.Type) && raw.Item.ID != "" {
			a.tools[raw.Item.ID] = true
		}
	}
}
func isTool(t string) bool {
	switch t {
	case "command_execution", "mcp_tool_call", "web_search", "file_change":
		return true
	}
	return false
}
func (a *usageAccumulator) Usage() *harness.Usage {
	if !a.hasInput && !a.hasOutput && a.turns == 0 && len(a.tools) == 0 {
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
	if a.turns > 0 || len(a.tools) > 0 {
		u.ToolCalls = harness.Int(len(a.tools))
	}
	return &u
}
