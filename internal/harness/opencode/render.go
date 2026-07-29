package opencode

import (
	"encoding/json"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// renderEvent is the display-only view of `opencode run --format json`.
// Tool work arrives as type "tool_use" with the call's state nested under
// part.state (see testdata/multistep_deny.jsonl).
type renderEvent struct {
	Type string `json:"type"`
	Part struct {
		Text  string `json:"text"`
		Tool  string `json:"tool"`
		State struct {
			Title string `json:"title"`
		} `json:"state"`
	} `json:"part"`
	Error struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
}

// RenderEvent maps one opencode run --format json line to displayable
// events. step_start and step_finish carry only bookkeeping and render
// nothing.
func (Opencode) RenderEvent(line []byte) []harness.Event {
	var evt renderEvent
	if json.Unmarshal(line, &evt) != nil {
		return nil
	}
	switch evt.Type {
	case "text":
		if evt.Part.Text == "" {
			return nil
		}
		return []harness.Event{{Kind: harness.EventText, Summary: evt.Part.Text}}
	case "tool_use":
		return []harness.Event{{
			Kind: harness.EventTool, Tool: evt.Part.Tool,
			Summary: harness.FirstLine(evt.Part.State.Title),
		}}
	case "error":
		message := evt.Error.Data.Message
		if message == "" {
			message = evt.Error.Name
		}
		if message == "" {
			return nil
		}
		return []harness.Event{{Kind: harness.EventError, Summary: harness.FirstLine(message)}}
	}
	return nil
}
