package claude

import (
	"encoding/json"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// renderEvent is the subset of stream-json needed to display a live feed.
// It is separate from streamEvent (parse.go) on purpose: that type is the
// contract for extracting a run's result and should not grow display-only
// fields.
type renderEvent struct {
	Type    string   `json:"type"`
	Result  string   `json:"result"`
	IsError bool     `json:"is_error"`
	Errors  []string `json:"errors"`
	Message struct {
		Content []contentBlock `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
	IsError bool            `json:"is_error"`
}

// toolSummaryKeys are the tool-input fields worth showing, most specific
// first. Claude Code's built-in tools each carry one of these; anything
// else renders as a bare tool name rather than guessing.
var toolSummaryKeys = []string{
	"file_path", "notebook_path", "path", "command", "pattern",
	"url", "query", "description", "prompt", "subagent_type",
}

// RenderEvent maps one stream-json line to displayable events.
func (Claude) RenderEvent(line []byte) []harness.Event {
	var evt renderEvent
	if json.Unmarshal(line, &evt) != nil {
		return nil
	}
	switch evt.Type {
	case "assistant":
		return assistantEvents(evt.Message.Content)
	case "user":
		return toolResultEvents(evt.Message.Content)
	case "result":
		return resultEvents(evt)
	}
	return nil
}

func assistantEvents(blocks []contentBlock) []harness.Event {
	var events []harness.Event
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if text := strings.TrimSpace(block.Text); text != "" {
				events = append(events, harness.Event{Kind: harness.EventText, Summary: text})
			}
		case "tool_use":
			events = append(events, harness.Event{
				Kind: harness.EventTool, Tool: block.Name, Summary: toolSummary(block.Input),
			})
		}
	}
	return events
}

// toolResultEvents surfaces only failed tool results. A successful one adds
// nothing the tool call did not already say, and its body would flood the
// feed.
func toolResultEvents(blocks []contentBlock) []harness.Event {
	var events []harness.Event
	for _, block := range blocks {
		if block.Type != "tool_result" || !block.IsError {
			continue
		}
		events = append(events, harness.Event{
			Kind: harness.EventError, Summary: harness.FirstLine(blockText(block.Content)),
		})
	}
	return events
}

func resultEvents(evt renderEvent) []harness.Event {
	if evt.IsError {
		summary := strings.Join(evt.Errors, "; ")
		if summary == "" {
			summary = evt.Result
		}
		return []harness.Event{{Kind: harness.EventError, Summary: summary}}
	}
	if evt.Result == "" {
		return nil
	}
	return []harness.Event{{Kind: harness.EventResult, Summary: evt.Result}}
}

func toolSummary(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var fields map[string]any
	if json.Unmarshal(input, &fields) != nil {
		return ""
	}
	for _, key := range toolSummaryKeys {
		if value, ok := fields[key].(string); ok && value != "" {
			return harness.FirstLine(value)
		}
	}
	return ""
}

// blockText reads a content field that the API sends either as a bare
// string or as an array of typed blocks.
func blockText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, " ")
}
