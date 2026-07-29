package codex

import (
	"encoding/json"
	"fmt"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// renderEvent is the display-only view of `codex exec --json`. Item shapes
// vary by type (codex-rs/exec/src/exec_events.rs); unknown ones degrade to
// a bare type name rather than guessing at fields.
type renderEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Error   struct {
		Message string `json:"message"`
	} `json:"error"`
	Item struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Command string `json:"command"`
		Server  string `json:"server"`
		Tool    string `json:"tool"`
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
		// A failed command reports itself through an exit code rather than
		// the error field every other item type uses. Decoded as a pointer
		// so an absent field stays silent instead of reading as success.
		ExitCode         *int   `json:"exit_code"`
		AggregatedOutput string `json:"aggregated_output"`
	} `json:"item"`
}

// RenderEvent maps one codex exec --json line to displayable events. Tool
// work renders when it *starts*, which is the point of watching live;
// completions render only when they carry a failure.
func (Codex) RenderEvent(line []byte) []harness.Event {
	var evt renderEvent
	if json.Unmarshal(line, &evt) != nil {
		return nil
	}
	switch evt.Type {
	case "error":
		return errorEvent(evt.Message)
	case "turn.failed":
		return errorEvent(evt.Error.Message)
	case "item.started":
		return startedItemEvent(evt)
	case "item.completed":
		return completedItemEvent(evt)
	}
	return nil
}

func startedItemEvent(evt renderEvent) []harness.Event {
	switch evt.Item.Type {
	case "agent_message", "reasoning":
		// Text arrives on completion; reasoning is internal noise.
		return nil
	case "command_execution":
		return []harness.Event{{
			Kind: harness.EventTool, Tool: "bash",
			Summary: harness.FirstLine(evt.Item.Command),
		}}
	case "mcp_tool_call":
		tool := evt.Item.Tool
		if evt.Item.Server != "" {
			tool = evt.Item.Server + "/" + evt.Item.Tool
		}
		return []harness.Event{{Kind: harness.EventTool, Tool: tool}}
	default:
		return []harness.Event{{Kind: harness.EventTool, Tool: evt.Item.Type}}
	}
}

func completedItemEvent(evt renderEvent) []harness.Event {
	switch evt.Item.Type {
	case "agent_message":
		if evt.Item.Text == "" {
			return nil
		}
		return []harness.Event{{Kind: harness.EventText, Summary: evt.Item.Text}}
	case "error":
		return errorEvent(evt.Item.Message)
	case "command_execution":
		if evt.Item.ExitCode == nil || *evt.Item.ExitCode == 0 {
			return nil
		}
		summary := harness.FirstLine(evt.Item.AggregatedOutput)
		if summary == "" {
			summary = fmt.Sprintf("command exited %d", *evt.Item.ExitCode)
		}
		return []harness.Event{{Kind: harness.EventError, Summary: summary}}
	default:
		return errorEvent(evt.Item.Error.Message)
	}
}

func errorEvent(message string) []harness.Event {
	if message == "" {
		return nil
	}
	return []harness.Event{{Kind: harness.EventError, Summary: harness.FirstLine(message)}}
}
