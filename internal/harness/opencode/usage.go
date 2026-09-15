package opencode

import (
	"encoding/json"

	"github.com/blackpaw-studio/leo/internal/harness"
)

type usageAccumulator struct {
	steps, finishes, tools map[string]bool
	input, output          int64
	hasInput, hasOutput    bool
}

func (Opencode) NewUsageAccumulator() harness.UsageAccumulator {
	return &usageAccumulator{steps: map[string]bool{}, finishes: map[string]bool{}, tools: map[string]bool{}}
}
func (a *usageAccumulator) AddLine(line []byte) {
	var raw struct {
		Type string `json:"type"`
		Part struct {
			ID, Type string
			Tokens   struct {
				Input     *int64 `json:"input"`
				Output    *int64 `json:"output"`
				Reasoning *int64 `json:"reasoning"`
				Cache     struct {
					Read  *int64 `json:"read"`
					Write *int64 `json:"write"`
				} `json:"cache"`
			} `json:"tokens"`
		} `json:"part"`
	}
	if json.Unmarshal(line, &raw) != nil {
		return
	}
	if raw.Type == "step_start" && raw.Part.ID != "" {
		a.steps[raw.Part.ID] = true
	}
	if raw.Type == "step_finish" && (raw.Part.ID == "" || !a.finishes[raw.Part.ID]) {
		if raw.Part.ID != "" {
			a.finishes[raw.Part.ID] = true
		}
		if raw.Part.Tokens.Input != nil {
			a.input += *raw.Part.Tokens.Input
			a.hasInput = true
		}
		if raw.Part.Tokens.Cache.Read != nil {
			a.input += *raw.Part.Tokens.Cache.Read
			a.hasInput = true
		}
		if raw.Part.Tokens.Cache.Write != nil {
			a.input += *raw.Part.Tokens.Cache.Write
			a.hasInput = true
		}
		if raw.Part.Tokens.Output != nil || raw.Part.Tokens.Reasoning != nil {
			if raw.Part.Tokens.Output != nil {
				a.output += *raw.Part.Tokens.Output
			}
			if raw.Part.Tokens.Reasoning != nil {
				a.output += *raw.Part.Tokens.Reasoning
			}
			a.hasOutput = true
		}
	}
	if raw.Type == "tool_use" && raw.Part.Type == "tool" && raw.Part.ID != "" {
		a.tools[raw.Part.ID] = true
	}
}
func (a *usageAccumulator) Usage() *harness.Usage {
	if !a.hasInput && !a.hasOutput && len(a.steps) == 0 && len(a.tools) == 0 {
		return nil
	}
	u := harness.Usage{}
	if a.hasInput {
		u.InputTokens = harness.Int64(a.input)
	}
	if a.hasOutput {
		u.OutputTokens = harness.Int64(a.output)
	}
	if len(a.steps) > 0 {
		u.Turns = harness.Int(len(a.steps))
	}
	if len(a.steps) > 0 || len(a.tools) > 0 {
		u.ToolCalls = harness.Int(len(a.tools))
	}
	return &u
}
