package consult

import (
	"encoding/json"
	"time"
)

// StreamEvent is one recorded line of a consult's stream, read back for
// rendering.
type StreamEvent struct {
	// Offset is how far into the consult the event landed.
	Offset time.Duration
	// Data is the harness event verbatim, or nil when the line was not
	// JSON and Raw carries it instead.
	Data json.RawMessage
	Raw  string
}

// DecodeEvent decodes one complete framed line. It reports false for a line
// that does not decode: a reader tailing a stream the daemon is still
// appending to can legitimately see a torn line, and skipping it is better
// than aborting the feed.
//
// Callers own line splitting, because following a growing file means holding
// a partial trailing line until its newline arrives.
func DecodeEvent(line []byte) (StreamEvent, bool) {
	var ev streamEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return StreamEvent{}, false
	}
	return StreamEvent{
		Offset: time.Duration(ev.T * float64(time.Second)),
		Data:   ev.D,
		Raw:    ev.Raw,
	}, true
}
