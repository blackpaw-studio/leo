package consult

import (
	"encoding/json"
	"time"
)

type Entry struct {
	ID           string        `json:"id"`
	Status       Status        `json:"status"`
	Elapsed      time.Duration `json:"elapsed"`
	Active       time.Duration `json:"active"`
	Text         string        `json:"text,omitempty"`
	Err          string        `json:"error,omitempty"`
	TurnID       string        `json:"turn_id,omitempty"`
	Outcome      TurnOutcome   `json:"outcome,omitempty"`
	Delivered    bool          `json:"delivered,omitempty"`
	Stalled      bool          `json:"stalled,omitempty"`
	InputTokens  *int64        `json:"input_tokens,omitempty"`
	OutputTokens *int64        `json:"output_tokens,omitempty"`
	CostUSD      *float64      `json:"cost_usd,omitempty"`
	UsageTurns   *int          `json:"usage_turns,omitempty"`
	ToolCalls    *int          `json:"tool_calls,omitempty"`
	Worktree     string        `json:"worktree,omitempty"`
	Branch       string        `json:"branch,omitempty"`
}

// MarshalJSON exposes elapsed and active time in seconds for API clients.
func (e Entry) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID             string      `json:"id"`
		Status         Status      `json:"status"`
		ElapsedSeconds float64     `json:"elapsed_seconds"`
		ActiveSeconds  float64     `json:"active_seconds"`
		Text           string      `json:"text,omitempty"`
		Err            string      `json:"error,omitempty"`
		TurnID         string      `json:"turn_id,omitempty"`
		Outcome        TurnOutcome `json:"outcome,omitempty"`
		Delivered      bool        `json:"delivered,omitempty"`
		Stalled        bool        `json:"stalled,omitempty"`
		InputTokens    *int64      `json:"input_tokens,omitempty"`
		OutputTokens   *int64      `json:"output_tokens,omitempty"`
		CostUSD        *float64    `json:"cost_usd,omitempty"`
		UsageTurns     *int        `json:"usage_turns,omitempty"`
		ToolCalls      *int        `json:"tool_calls,omitempty"`
		Worktree       string      `json:"worktree,omitempty"`
		Branch         string      `json:"branch,omitempty"`
	}{e.ID, e.Status, e.Elapsed.Seconds(), e.Active.Seconds(), e.Text, e.Err, e.TurnID, e.Outcome, e.Delivered, e.Stalled, e.InputTokens, e.OutputTokens, e.CostUSD, e.UsageTurns, e.ToolCalls, e.Worktree, e.Branch})
}

// UnmarshalJSON restores the durations used by CLI and MCP clients.
func (e *Entry) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID             string      `json:"id"`
		Status         Status      `json:"status"`
		ElapsedSeconds float64     `json:"elapsed_seconds"`
		ActiveSeconds  float64     `json:"active_seconds"`
		Text           string      `json:"text"`
		Err            string      `json:"error"`
		TurnID         string      `json:"turn_id"`
		Outcome        TurnOutcome `json:"outcome"`
		Delivered      bool        `json:"delivered"`
		Stalled        bool        `json:"stalled"`
		InputTokens    *int64      `json:"input_tokens"`
		OutputTokens   *int64      `json:"output_tokens"`
		CostUSD        *float64    `json:"cost_usd"`
		UsageTurns     *int        `json:"usage_turns"`
		ToolCalls      *int        `json:"tool_calls"`
		Worktree       string      `json:"worktree"`
		Branch         string      `json:"branch"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	e.ID, e.Status = wire.ID, wire.Status
	e.Elapsed = time.Duration(wire.ElapsedSeconds * float64(time.Second))
	e.Active = time.Duration(wire.ActiveSeconds * float64(time.Second))
	e.Text, e.Err, e.TurnID = wire.Text, wire.Err, wire.TurnID
	e.Outcome, e.Delivered, e.Stalled = wire.Outcome, wire.Delivered, wire.Stalled
	e.InputTokens, e.OutputTokens, e.CostUSD = wire.InputTokens, wire.OutputTokens, wire.CostUSD
	e.UsageTurns, e.ToolCalls, e.Worktree, e.Branch = wire.UsageTurns, wire.ToolCalls, wire.Worktree, wire.Branch
	return nil
}
