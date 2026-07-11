package opencode

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// event is the subset of `opencode run --format json` JSONL events leo
// consumes. Every event carries the session ID; text lives in part.text.
type event struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Part      struct {
		Text string `json:"text"`
	} `json:"part"`
	Error struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
}

// ParseEvents folds an opencode run --format json stream into a Result.
// Text events accumulate (joined with newlines) — multi-step turns emit
// several. EOF is authoritative end-of-turn: the final step_finish may be
// absent (upstream #26855 on older versions and --attach), so it is never
// required. Non-JSON lines (opencode interleaves log output on errors) are
// skipped.
func (Opencode) ParseEvents(r io.Reader) (harness.Result, error) {
	output, err := io.ReadAll(r)
	if err != nil {
		return harness.Result{}, err
	}
	var res harness.Result
	var texts []string
	for _, line := range bytes.Split(output, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var evt event
		if json.Unmarshal(line, &evt) != nil {
			continue
		}
		if res.SessionID == "" && evt.SessionID != "" {
			res.SessionID = evt.SessionID
		}
		switch evt.Type {
		case "text":
			if evt.Part.Text != "" {
				texts = append(texts, evt.Part.Text)
			}
		case "error":
			res.IsError = true
			msg := evt.Error.Data.Message
			if msg == "" {
				msg = evt.Error.Name
			}
			if msg != "" {
				res.Errors = append(res.Errors, msg)
			}
		}
	}
	res.Text = strings.Join(texts, "\n")
	return res, nil
}
