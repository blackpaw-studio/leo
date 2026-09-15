package opencode

import (
	"encoding/json"

	"github.com/blackpaw-studio/leo/internal/harness"
)

type usageAccumulator struct {
	steps, finishes, tools harness.UsageIDs
	input, output          int64
	hasInput, hasOutput    bool
	incomplete             bool
}

func (Opencode) NewUsageAccumulator() harness.UsageAccumulator {
	return &usageAccumulator{steps: harness.NewUsageIDs(), finishes: harness.NewUsageIDs(), tools: harness.NewUsageIDs()}
}
func (a *usageAccumulator) AddLine(line []byte) {
	var shape struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(line, &shape) != nil {
		return
	}
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
		if shape.Type == "step_finish" {
			a.incomplete = true
		}
		return
	}
	if raw.Type == "step_start" && raw.Part.ID != "" {
		a.steps.Add(raw.Part.ID)
	}
	if raw.Type == "step_finish" && (raw.Part.ID == "" || a.finishes.Add(raw.Part.ID)) {
		if raw.Part.Tokens.Input == nil && raw.Part.Tokens.Cache.Read == nil && raw.Part.Tokens.Cache.Write == nil && raw.Part.Tokens.Output == nil && raw.Part.Tokens.Reasoning == nil {
			a.incomplete = true
		}
		inputDelta, inputOK := sumValues(raw.Part.Tokens.Input, raw.Part.Tokens.Cache.Read, raw.Part.Tokens.Cache.Write)
		outputDelta, outputOK := sumValues(raw.Part.Tokens.Output, raw.Part.Tokens.Reasoning)
		nextInput, inputAddOK := harness.AddInt64(a.input, inputDelta)
		nextOutput, outputAddOK := harness.AddInt64(a.output, outputDelta)
		if !inputOK || !outputOK || !inputAddOK || !outputAddOK {
			a.incomplete = true
		} else {
			if raw.Part.Tokens.Input != nil || raw.Part.Tokens.Cache.Read != nil || raw.Part.Tokens.Cache.Write != nil {
				a.input, a.hasInput = nextInput, true
			}
			if raw.Part.Tokens.Output != nil || raw.Part.Tokens.Reasoning != nil {
				a.output, a.hasOutput = nextOutput, true
			}
		}
	}
	if raw.Type == "tool_use" && raw.Part.Type == "tool" && raw.Part.ID != "" {
		a.tools.Add(raw.Part.ID)
	}
	if a.steps.Overflow || a.finishes.Overflow || a.tools.Overflow {
		a.incomplete = true
	}
}

func sumValues(values ...*int64) (int64, bool) {
	var total int64
	for _, value := range values {
		if value == nil {
			continue
		}
		var ok bool
		total, ok = harness.AddInt64(total, *value)
		if !ok {
			return 0, false
		}
	}
	return total, true
}
func (a *usageAccumulator) Usage() *harness.Usage {
	incomplete := a.incomplete || a.steps.Len() > a.finishes.Len()
	if !a.hasInput && !a.hasOutput && a.steps.Len() == 0 && a.tools.Len() == 0 && !incomplete {
		return nil
	}
	u := harness.Usage{}
	if a.hasInput {
		u.InputTokens = harness.Int64(a.input)
	}
	if a.hasOutput {
		u.OutputTokens = harness.Int64(a.output)
	}
	if a.steps.Len() > 0 {
		u.Turns = harness.Int(a.steps.Len())
	}
	if a.steps.Len() > 0 || a.tools.Len() > 0 {
		u.ToolCalls = harness.Int(a.tools.Len())
	}
	u.Incomplete = incomplete
	return &u
}
