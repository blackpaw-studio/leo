package consult

import (
	"encoding/json"
	"time"
)

type Entry struct {
	ID        string        `json:"id"`
	Status    Status        `json:"status"`
	Elapsed   time.Duration `json:"elapsed"`
	Active    time.Duration `json:"active"`
	Text      string        `json:"text,omitempty"`
	Err       string        `json:"error,omitempty"`
	TurnID    string        `json:"turn_id,omitempty"`
	Outcome   TurnOutcome   `json:"outcome,omitempty"`
	Delivered bool          `json:"delivered,omitempty"`
	Stalled   bool          `json:"stalled,omitempty"`
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
	}{e.ID, e.Status, e.Elapsed.Seconds(), e.Active.Seconds(), e.Text, e.Err, e.TurnID, e.Outcome, e.Delivered, e.Stalled})
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
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	e.ID, e.Status = wire.ID, wire.Status
	e.Elapsed = time.Duration(wire.ElapsedSeconds * float64(time.Second))
	e.Active = time.Duration(wire.ActiveSeconds * float64(time.Second))
	e.Text, e.Err, e.TurnID = wire.Text, wire.Err, wire.TurnID
	e.Outcome, e.Delivered, e.Stalled = wire.Outcome, wire.Delivered, wire.Stalled
	return nil
}
