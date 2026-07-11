package claude

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// claudeResult is the minimal structure for parsing the final "result" event
// from claude --output-format stream-json (newline-delimited JSON).
type claudeResult struct {
	SessionID string   `json:"session_id"`
	Result    string   `json:"result"`
	IsError   bool     `json:"is_error"`
	Errors    []string `json:"errors"`
}

// streamEvent represents a single event line from stream-json output.
type streamEvent struct {
	Type string `json:"type"`
	claudeResult
}

// ParseEvents extracts the final result from stream-json (NDJSON) output.
// It scans for the last line with "type":"result"; falls back to parsing the
// whole payload as a single JSON object (old --output-format json).
func (Claude) ParseEvents(r io.Reader) (harness.Result, error) {
	output, err := io.ReadAll(r)
	if err != nil {
		return harness.Result{}, err
	}
	var best claudeResult
	for _, line := range bytes.Split(output, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var evt streamEvent
		if json.Unmarshal(line, &evt) == nil && evt.Type == "result" {
			best = evt.claudeResult
		}
	}
	if best.SessionID == "" && best.Result == "" && len(best.Errors) == 0 {
		// Fallback: single JSON object (old --output-format json).
		_ = json.Unmarshal(output, &best)
	}
	return harness.Result{
		SessionID: best.SessionID,
		Text:      best.Result,
		IsError:   best.IsError,
		Errors:    best.Errors,
	}, nil
}
