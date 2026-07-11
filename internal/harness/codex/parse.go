package codex

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// event is the subset of `codex exec --json` JSONL events leo consumes.
// Full schema: codex-rs/exec/src/exec_events.rs (thread.started,
// turn.started/completed/failed, item.started/updated/completed, error).
type event struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Message  string `json:"message"` // type=="error"
	Error    struct {
		Message string `json:"message"`
	} `json:"error"` // type=="turn.failed"
	Item struct {
		Type    string `json:"type"`
		Text    string `json:"text"`    // item type "agent_message"
		Message string `json:"message"` // item type "error"
		Error   struct {
			Message string `json:"message"`
		} `json:"error"` // e.g. failed mcp_tool_call
	} `json:"item"`
}

// ParseEvents folds a codex exec --json stream into a Result. The last
// agent_message wins as the result text. Fatal signals are top-level
// "error" events and "turn.failed"; item-level errors (including failed
// tool calls) are recorded but not fatal on their own — codex signals
// fatality via exit code and the top-level events. EOF ends the turn;
// unparseable lines are skipped.
func (Codex) ParseEvents(r io.Reader) (harness.Result, error) {
	output, err := io.ReadAll(r)
	if err != nil {
		return harness.Result{}, err
	}
	var res harness.Result
	for _, line := range bytes.Split(output, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var evt event
		if json.Unmarshal(line, &evt) != nil {
			continue
		}
		switch evt.Type {
		case "thread.started":
			res.SessionID = evt.ThreadID
		case "error":
			res.IsError = true
			if evt.Message != "" {
				res.Errors = append(res.Errors, evt.Message)
			}
		case "turn.failed":
			res.IsError = true
			if evt.Error.Message != "" {
				res.Errors = append(res.Errors, evt.Error.Message)
			}
		case "item.completed":
			switch evt.Item.Type {
			case "agent_message":
				res.Text = evt.Item.Text
			case "error":
				if evt.Item.Message != "" {
					res.Errors = append(res.Errors, evt.Item.Message)
				}
			default:
				if evt.Item.Error.Message != "" {
					res.Errors = append(res.Errors, evt.Item.Error.Message)
				}
			}
		}
	}
	return res, nil
}
