package bridge

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCommandWireFormat(t *testing.T) {
	cases := []struct {
		name string
		cmd  Command
		want string
	}{
		{
			name: "deliver as user",
			cmd:  withID(Deliver("hello <world> & \"you\"\nline two", true), "c1"),
			want: `{"id":"c1","op":"deliver","text":"hello <world> & \"you\"\nline two","as_user":true}`,
		},
		{
			name: "deliver as plugin keeps as_user false on the wire",
			cmd:  withID(Deliver("From agent x via leo: hi", false), "c2"),
			want: `{"id":"c2","op":"deliver","text":"From agent x via leo: hi","as_user":false}`,
		},
		{
			name: "compact with instructions",
			cmd:  withID(Compact("keep the plan"), "c3"),
			want: `{"id":"c3","op":"compact","instructions":"keep the plan"}`,
		},
		{
			name: "compact without instructions omits the field",
			cmd:  withID(Compact(""), "c4"),
			want: `{"id":"c4","op":"compact"}`,
		},
		{
			name: "clear",
			cmd:  withID(Clear(), "c5"),
			want: `{"id":"c5","op":"clear"}`,
		},
		{
			name: "interrupt",
			cmd:  withID(Interrupt(), "c6"),
			want: `{"id":"c6","op":"interrupt"}`,
		},
		{
			name: "fields foreign to the op never reach the wire",
			cmd:  Command{ID: "c7", Op: OpClear, Text: "stray", AsUser: true, Instructions: "stray"},
			want: `{"id":"c7","op":"clear"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.cmd.MarshalLine()
			if err != nil {
				t.Fatalf("MarshalLine: %v", err)
			}
			if string(got) != tc.want+"\n" {
				t.Fatalf("wire mismatch\n got: %s\nwant: %s", got, tc.want+"\n")
			}
			if strings.Count(string(got), "\n") != 1 {
				t.Fatalf("a command must be exactly one line, got %q", got)
			}
		})
	}
}

func TestCommandValidate(t *testing.T) {
	cases := []struct {
		name    string
		cmd     Command
		wantErr bool
	}{
		{"deliver", Deliver("x", true), false},
		{"deliver empty text", Deliver("", true), true},
		{"compact", Compact(""), false},
		{"clear", Clear(), false},
		{"interrupt", Interrupt(), false},
		{"unknown op", Command{Op: "reboot"}, true},
		{"empty op", Command{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cmd.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidCommand) {
				t.Fatalf("Validate() err=%v, want wrapping ErrInvalidCommand", err)
			}
		})
	}
}

func TestParseReportAccepts(t *testing.T) {
	cases := []struct {
		name string
		body string
		want Report
	}{
		{
			name: "hello",
			body: `{"type":"hello","session_id":"s-1","claude_version":"2.1.287"}`,
			want: Report{Type: ReportHello, SessionID: "s-1", ClaudeVersion: "2.1.287"},
		},
		{
			name: "hello saying busy",
			body: `{"type":"hello","session_id":"s-1","claude_version":"2.1.289","busy":true}`,
			want: Report{Type: ReportHello, SessionID: "s-1", ClaudeVersion: "2.1.289", Busy: boolPtr(true)},
		},
		{
			name: "hello saying idle",
			body: `{"type":"hello","session_id":"s-1","claude_version":"2.1.289","busy":false}`,
			want: Report{Type: ReportHello, SessionID: "s-1", ClaudeVersion: "2.1.289", Busy: boolPtr(false)},
		},
		{
			name: "ack ok",
			body: `{"type":"ack","id":"c1","ok":true}`,
			want: Report{Type: ReportAck, ID: "c1", OK: true},
		},
		{
			name: "ack failure with error",
			body: `{"type":"ack","id":"c1","ok":false,"error":"busy"}`,
			want: Report{Type: ReportAck, ID: "c1", OK: false, Error: "busy"},
		},
		{
			name: "turn.start",
			body: `{"type":"event","name":"turn.start"}`,
			want: Report{Type: ReportEvent, Name: EventTurnStart},
		},
		{
			name: "turn.complete with usage",
			body: `{"type":"event","name":"turn.complete","usage":{"context":{"used":10}}}`,
			want: Report{Type: ReportEvent, Name: EventTurnComplete, Usage: json.RawMessage(`{"context":{"used":10}}`)},
		},
		{
			name: "turn.complete with null usage treated as absent",
			body: `{"type":"event","name":"turn.complete","usage":null}`,
			want: Report{Type: ReportEvent, Name: EventTurnComplete},
		},
		{
			name: "session.end with reason",
			body: `{"type":"event","name":"session.end","reason":"prompt_input_exit"}`,
			want: Report{Type: ReportEvent, Name: EventSessionEnd, Reason: "prompt_input_exit"},
		},
		{
			name: "turn.start with its prompt",
			body: `{"type":"event","name":"turn.start","prompt":"do the thing\nnow"}`,
			want: Report{Type: ReportEvent, Name: EventTurnStart, Prompt: "do the thing\nnow"},
		},
		{
			name: "turn.start with an empty prompt (a continuation)",
			body: `{"type":"event","name":"turn.start","prompt":""}`,
			want: Report{Type: ReportEvent, Name: EventTurnStart},
		},
		{
			name: "turn.start with an event id",
			body: `{"type":"event","name":"turn.start","event_id":"turn.start:t1"}`,
			want: Report{Type: ReportEvent, Name: EventTurnStart, EventID: "turn.start:t1"},
		},
		{
			name: "turn.complete with its final message and usage",
			body: `{"type":"event","name":"turn.complete","message":"all done","usage":{"u":1}}`,
			want: Report{Type: ReportEvent, Name: EventTurnComplete, Message: "all done", Usage: json.RawMessage(`{"u":1}`)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseReport([]byte(tc.body))
			if err != nil {
				t.Fatalf("ParseReport: %v", err)
			}
			if got.Type != tc.want.Type || got.SessionID != tc.want.SessionID ||
				got.ClaudeVersion != tc.want.ClaudeVersion || got.ID != tc.want.ID ||
				got.OK != tc.want.OK || got.Error != tc.want.Error || got.Name != tc.want.Name ||
				got.Reason != tc.want.Reason || string(got.Usage) != string(tc.want.Usage) ||
				!sameBoolPtr(got.Busy, tc.want.Busy) || got.Prompt != tc.want.Prompt ||
				got.Message != tc.want.Message || got.EventID != tc.want.EventID {
				t.Fatalf("ParseReport mismatch\n got: %+v\nwant: %+v", got, tc.want)
			}
		})
	}
}

func TestParseReportRejects(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty body", ``},
		{"not json", `hello`},
		{"array", `[{"type":"ack"}]`},
		{"trailing value", `{"type":"ack","id":"c1","ok":true} {}`},
		{"missing type", `{"id":"c1","ok":true}`},
		{"unknown type", `{"type":"bogus"}`},
		{"type wrong kind", `{"type":7}`},
		{"hello missing session_id", `{"type":"hello","claude_version":"2.1.287"}`},
		{"hello empty session_id", `{"type":"hello","session_id":"","claude_version":"2.1.287"}`},
		{"hello missing claude_version", `{"type":"hello","session_id":"s"}`},
		{"hello with ack field", `{"type":"hello","session_id":"s","claude_version":"v","id":"c1"}`},
		{"hello unknown field", `{"type":"hello","session_id":"s","claude_version":"v","extra":1}`},
		{"hello busy wrong kind", `{"type":"hello","session_id":"s","claude_version":"v","busy":"yes"}`},
		{"hello busy null", `{"type":"hello","session_id":"s","claude_version":"v","busy":null}`},
		{"ack missing id", `{"type":"ack","ok":true}`},
		{"ack empty id", `{"type":"ack","id":"","ok":true}`},
		{"ack missing ok", `{"type":"ack","id":"c1"}`},
		{"ack ok wrong kind", `{"type":"ack","id":"c1","ok":"yes"}`},
		{"ack null ok", `{"type":"ack","id":"c1","ok":null}`},
		{"ack with event field", `{"type":"ack","id":"c1","ok":true,"name":"turn.start"}`},
		{"event missing name", `{"type":"event"}`},
		{"event unknown name", `{"type":"event","name":"turn.middle"}`},
		{"event usage not an object", `{"type":"event","name":"turn.complete","usage":[1,2]}`},
		{"event usage scalar", `{"type":"event","name":"turn.complete","usage":3}`},
		{"event reason wrong kind", `{"type":"event","name":"session.end","reason":5}`},
		{"event with hello field", `{"type":"event","name":"turn.start","session_id":"s"}`},
		{"prompt on turn.complete", `{"type":"event","name":"turn.complete","prompt":"p"}`},
		{"prompt on session.end", `{"type":"event","name":"session.end","prompt":"p"}`},
		{"message on turn.start", `{"type":"event","name":"turn.start","message":"m"}`},
		{"message on session.end", `{"type":"event","name":"session.end","message":"m"}`},
		{"prompt wrong kind", `{"type":"event","name":"turn.start","prompt":7}`},
		{"prompt null", `{"type":"event","name":"turn.start","prompt":null}`},
		{"message wrong kind", `{"type":"event","name":"turn.complete","message":{"text":"m"}}`},
		{"event id wrong kind", `{"type":"event","name":"turn.start","event_id":1}`},
		{"event id on an ack", `{"type":"ack","id":"c1","ok":true,"event_id":"x"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseReport([]byte(tc.body))
			if err == nil {
				t.Fatalf("ParseReport(%s) accepted, want rejection", tc.body)
			}
			if !errors.Is(err, ErrInvalidReport) {
				t.Fatalf("ParseReport err=%v, want wrapping ErrInvalidReport", err)
			}
		})
	}
}

// withID returns cmd with its id set, as the hub would assign it.
func withID(cmd Command, id string) Command {
	cmd.ID = id
	return cmd
}

func boolPtr(b bool) *bool { return &b }

func sameBoolPtr(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
