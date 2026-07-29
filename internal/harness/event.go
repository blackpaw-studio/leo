package harness

import "strings"

// EventKind classifies a rendered stream event for display.
type EventKind string

const (
	EventText   EventKind = "text"
	EventTool   EventKind = "tool"
	EventResult EventKind = "result"
	EventError  EventKind = "error"
)

// Event is one displayable moment from a harness's one-shot stream, used to
// render a live feed (`leo consult watch`). It is deliberately lossy:
// ParseEvents remains the authority on what a run actually produced.
type Event struct {
	Kind EventKind
	// Tool names the tool being invoked, for EventTool.
	Tool string
	// Summary is a single line for tools and errors, and the full body for
	// EventText and EventResult. Callers own wrapping.
	Summary string
}

// EventRenderer is an optional harness capability: mapping one raw line of
// its one-shot stream to displayable events. A harness that does not
// implement it makes callers fall back to printing raw lines.
//
// One line can carry several events — a claude assistant message interleaves
// text and tool calls — and a line with nothing worth showing yields none.
type EventRenderer interface {
	RenderEvent(line []byte) []Event
}

// FirstLine collapses a value to its first non-empty line, for summaries
// that must stay on one row.
func FirstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}
