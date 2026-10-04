package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Command ops (daemon → mod). The wire shape of each is fixed by the mod
// contract in docs/specs/2026-10-03-claude-mod-bridge.md.
const (
	OpDeliver   = "deliver"
	OpCompact   = "compact"
	OpClear     = "clear"
	OpInterrupt = "interrupt"
)

// Report types (mod → daemon).
const (
	ReportHello = "hello"
	ReportAck   = "ack"
	ReportEvent = "event"
)

// Event names carried by a ReportEvent.
const (
	EventTurnStart    = "turn.start"
	EventTurnComplete = "turn.complete"
	EventSessionEnd   = "session.end"
)

var (
	// ErrInvalidCommand wraps every Command.Validate failure.
	ErrInvalidCommand = errors.New("invalid bridge command")
	// ErrInvalidReport wraps every ParseReport failure.
	ErrInvalidReport = errors.New("invalid bridge report")
)

// Command is one instruction for the mod. ID is assigned by the Hub on
// enqueue; callers build commands with Deliver, Compact, Clear or Interrupt.
// Fields foreign to Op are ignored on the wire.
type Command struct {
	ID           string
	Op           string
	Text         string // deliver
	AsUser       bool   // deliver
	Instructions string // compact, optional
}

// Deliver submits text as a prompt. asUser=true frames it as the user's own
// prompt; false lets Claude frame it as a plugin-sent message.
func Deliver(text string, asUser bool) Command {
	return Command{Op: OpDeliver, Text: text, AsUser: asUser}
}

// Compact compacts the conversation, with optional focus instructions.
func Compact(instructions string) Command {
	return Command{Op: OpCompact, Instructions: instructions}
}

// Clear starts a fresh conversation.
func Clear() Command { return Command{Op: OpClear} }

// Interrupt aborts the running turn.
func Interrupt() Command { return Command{Op: OpInterrupt} }

// Validate reports whether c is a command the mod can execute. It does not
// check ID, which the Hub assigns.
func (c Command) Validate() error {
	switch c.Op {
	case OpDeliver:
		if c.Text == "" {
			return fmt.Errorf("%w: deliver requires text", ErrInvalidCommand)
		}
		return nil
	case OpCompact, OpClear, OpInterrupt:
		return nil
	case "":
		return fmt.Errorf("%w: missing op", ErrInvalidCommand)
	default:
		return fmt.Errorf("%w: unknown op %q", ErrInvalidCommand, c.Op)
	}
}

// Wire shapes, one per op, so each emits exactly its contract fields. In
// particular as_user is always present on deliver, false included.
type (
	wireDeliver struct {
		ID     string `json:"id"`
		Op     string `json:"op"`
		Text   string `json:"text"`
		AsUser bool   `json:"as_user"`
	}
	wireCompact struct {
		ID           string `json:"id"`
		Op           string `json:"op"`
		Instructions string `json:"instructions,omitempty"`
	}
	wireBare struct {
		ID string `json:"id"`
		Op string `json:"op"`
	}
)

func (c Command) wire() any {
	switch c.Op {
	case OpDeliver:
		return wireDeliver{ID: c.ID, Op: c.Op, Text: c.Text, AsUser: c.AsUser}
	case OpCompact:
		return wireCompact{ID: c.ID, Op: c.Op, Instructions: c.Instructions}
	default:
		return wireBare{ID: c.ID, Op: c.Op}
	}
}

// MarshalJSON emits the contract shape for c's op.
func (c Command) MarshalJSON() ([]byte, error) {
	line, err := c.MarshalLine()
	if err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(line, []byte("\n")), nil
}

// MarshalLine encodes c as one NDJSON line, newline included. JSON string
// escaping keeps any newline inside Text off the framing layer. HTML
// escaping is off so the text reads verbatim in logs.
func (c Command) MarshalLine() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(c.wire()); err != nil {
		return nil, fmt.Errorf("encoding bridge command %s: %w", c.ID, err)
	}
	return buf.Bytes(), nil
}

// Report is one decoded mod → daemon report. Only the fields of its Type
// are populated.
type Report struct {
	Type string

	// hello
	SessionID     string
	ClaudeVersion string
	// Busy is the mod's own view of whether a turn is running; nil when the
	// hello did not say.
	Busy *bool

	// ack
	ID    string
	OK    bool
	Error string

	// event
	Name    string
	Usage   json.RawMessage // a JSON object, or nil when absent
	Reason  string
	Prompt  string // turn.start: the prompt the turn began with
	Message string // turn.complete: the assistant's final message
	// EventID is stable across the mod's retries of one event, so a replay
	// can be told from a new event; empty when the mod had none to give.
	EventID string
}

// reportKeys is the closed set of keys each report type may carry.
var reportKeys = map[string]map[string]bool{
	ReportHello: {"type": true, "session_id": true, "claude_version": true, "busy": true},
	ReportAck:   {"type": true, "id": true, "ok": true, "error": true},
	ReportEvent: {"type": true, "name": true, "event_id": true, "usage": true, "reason": true, "prompt": true, "message": true},
}

// eventOnlyKeys are event keys valid for a single event name: the prompt a
// turn began with, and the final message it ended on.
var eventOnlyKeys = map[string]string{
	"prompt":  EventTurnStart,
	"message": EventTurnComplete,
}

var eventNames = map[string]bool{
	EventTurnStart:    true,
	EventTurnComplete: true,
	EventSessionEnd:   true,
}

// ParseReport strictly decodes one report body: a single JSON object of a
// known type carrying only that type's keys, with required keys present and
// every value of the right kind. Anything else wraps ErrInvalidReport.
func ParseReport(body []byte) (Report, error) {
	fields, err := decodeObject(body)
	if err != nil {
		return Report{}, err
	}
	typ, err := stringField(fields, "type", true)
	if err != nil {
		return Report{}, err
	}
	allowed, ok := reportKeys[typ]
	if !ok {
		return Report{}, invalidReport("unknown type %q", typ)
	}
	for key := range fields {
		if !allowed[key] {
			return Report{}, invalidReport("key %q is not valid for %s reports", key, typ)
		}
	}
	switch typ {
	case ReportHello:
		return parseHello(fields)
	case ReportAck:
		return parseAck(fields)
	default:
		return parseEvent(fields)
	}
}

func parseHello(fields map[string]json.RawMessage) (Report, error) {
	sessionID, err := stringField(fields, "session_id", true)
	if err != nil {
		return Report{}, err
	}
	version, err := stringField(fields, "claude_version", true)
	if err != nil {
		return Report{}, err
	}
	var busy *bool
	if raw, present := fields["busy"]; present {
		var b bool
		if isNull(raw) || json.Unmarshal(raw, &b) != nil {
			return Report{}, invalidReport("hello busy must be a boolean")
		}
		busy = &b
	}
	return Report{Type: ReportHello, SessionID: sessionID, ClaudeVersion: version, Busy: busy}, nil
}

func parseAck(fields map[string]json.RawMessage) (Report, error) {
	id, err := stringField(fields, "id", true)
	if err != nil {
		return Report{}, err
	}
	raw, present := fields["ok"]
	var ok bool
	if !present || isNull(raw) || json.Unmarshal(raw, &ok) != nil {
		return Report{}, invalidReport("ack requires boolean ok")
	}
	msg, err := stringField(fields, "error", false)
	if err != nil {
		return Report{}, err
	}
	return Report{Type: ReportAck, ID: id, OK: ok, Error: msg}, nil
}

func parseEvent(fields map[string]json.RawMessage) (Report, error) {
	name, err := stringField(fields, "name", true)
	if err != nil {
		return Report{}, err
	}
	if !eventNames[name] {
		return Report{}, invalidReport("unknown event %q", name)
	}
	for key, only := range eventOnlyKeys {
		if _, present := fields[key]; present && name != only {
			return Report{}, invalidReport("key %q is only valid for %s events", key, only)
		}
	}
	reason, err := stringField(fields, "reason", false)
	if err != nil {
		return Report{}, err
	}
	prompt, err := stringField(fields, "prompt", false)
	if err != nil {
		return Report{}, err
	}
	message, err := stringField(fields, "message", false)
	if err != nil {
		return Report{}, err
	}
	eventID, err := stringField(fields, "event_id", false)
	if err != nil {
		return Report{}, err
	}
	var usage json.RawMessage
	if raw, present := fields["usage"]; present && !isNull(raw) {
		if _, err := decodeObject(raw); err != nil {
			return Report{}, invalidReport("usage must be a JSON object")
		}
		usage = append(json.RawMessage(nil), raw...)
	}
	return Report{Type: ReportEvent, Name: name, Usage: usage, Reason: reason, Prompt: prompt, Message: message, EventID: eventID}, nil
}

// decodeObject decodes exactly one JSON object, rejecting trailing values.
func decodeObject(body []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	var fields map[string]json.RawMessage
	if err := dec.Decode(&fields); err != nil {
		return nil, invalidReport("body must be a JSON object: %v", err)
	}
	if fields == nil {
		return nil, invalidReport("body must be a JSON object")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, invalidReport("body must hold exactly one JSON value")
	}
	return fields, nil
}

// stringField reads key as a JSON string. A required key must be present and
// non-empty; an optional key may be absent but must be a string if present.
func stringField(fields map[string]json.RawMessage, key string, required bool) (string, error) {
	raw, present := fields[key]
	if !present {
		if required {
			return "", invalidReport("missing %s", key)
		}
		return "", nil
	}
	var s string
	if isNull(raw) || json.Unmarshal(raw, &s) != nil {
		return "", invalidReport("%s must be a string", key)
	}
	if required && s == "" {
		return "", invalidReport("%s must not be empty", key)
	}
	return s, nil
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func invalidReport(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidReport, fmt.Sprintf(format, args...))
}
