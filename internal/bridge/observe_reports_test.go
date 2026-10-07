package bridge

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseReportObserveEvents(t *testing.T) {
	cases := []struct {
		name string
		body string
		want Report
	}{
		{
			name: "activity with tool and summary",
			body: `{"type":"event","name":"activity","tool":"Bash","summary":"go"}`,
			want: Report{Type: ReportEvent, Name: EventActivity, Activity: &ActivityReport{Tool: "Bash", Summary: "go"}},
		},
		{
			name: "activity cleared",
			body: `{"type":"event","name":"activity"}`,
			want: Report{Type: ReportEvent, Name: EventActivity, Activity: &ActivityReport{}},
		},
		{
			name: "attention needs input",
			body: `{"type":"event","name":"attention","state":"needs_input","kind":"permission","tool":"Bash","summary":"rm"}`,
			want: Report{Type: ReportEvent, Name: EventAttention, Attention: &AttentionReport{State: AttentionNeedsInput, Kind: AttentionPermission, Tool: "Bash", Summary: "rm"}},
		},
		{
			name: "attention cleared without kind",
			body: `{"type":"event","name":"attention","state":"cleared"}`,
			want: Report{Type: ReportEvent, Name: EventAttention, Attention: &AttentionReport{State: AttentionCleared}},
		},
		{
			name: "attention cleared with kind",
			body: `{"type":"event","name":"attention","state":"cleared","kind":"elicitation"}`,
			want: Report{Type: ReportEvent, Name: EventAttention, Attention: &AttentionReport{State: AttentionCleared, Kind: AttentionElicitation}},
		},
		{
			name: "subagents",
			body: `{"type":"event","name":"subagents","running":2}`,
			want: Report{Type: ReportEvent, Name: EventSubagents, Subagents: &SubagentsReport{Running: 2}},
		},
		{
			name: "subagents zero",
			body: `{"type":"event","name":"subagents","running":0,"event_id":"e1"}`,
			want: Report{Type: ReportEvent, Name: EventSubagents, EventID: "e1", Subagents: &SubagentsReport{Running: 0}},
		},
		{
			name: "compact started",
			body: `{"type":"event","name":"compact","phase":"started","trigger":"auto"}`,
			want: Report{Type: ReportEvent, Name: EventCompact, Compact: &CompactReport{Phase: CompactStarted, Trigger: CompactAuto}},
		},
		{
			name: "compact failed with error",
			body: `{"type":"event","name":"compact","phase":"failed","trigger":"manual","error":"too short"}`,
			want: Report{Type: ReportEvent, Name: EventCompact, Compact: &CompactReport{Phase: CompactFailed, Trigger: CompactManual, Error: "too short"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseReport([]byte(tc.body))
			if err != nil {
				t.Fatalf("ParseReport: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseReportRejectsBadObserveEvents(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"activity unknown key", `{"type":"event","name":"activity","tool":"Bash","input":"x"}`},
		{"activity tool not string", `{"type":"event","name":"activity","tool":3}`},
		{"activity with attention key", `{"type":"event","name":"activity","state":"cleared"}`},
		{"tool on turn.start", `{"type":"event","name":"turn.start","tool":"Bash"}`},
		{"attention missing state", `{"type":"event","name":"attention","kind":"permission"}`},
		{"attention bad state", `{"type":"event","name":"attention","state":"waiting","kind":"permission"}`},
		{"attention bad kind", `{"type":"event","name":"attention","state":"needs_input","kind":"nag"}`},
		{"attention needs input without kind", `{"type":"event","name":"attention","state":"needs_input"}`},
		{"attention with running", `{"type":"event","name":"attention","state":"cleared","running":1}`},
		{"subagents missing running", `{"type":"event","name":"subagents"}`},
		{"subagents negative", `{"type":"event","name":"subagents","running":-1}`},
		{"subagents fractional", `{"type":"event","name":"subagents","running":1.5}`},
		{"subagents null", `{"type":"event","name":"subagents","running":null}`},
		{"compact missing phase", `{"type":"event","name":"compact","trigger":"auto"}`},
		{"compact missing trigger", `{"type":"event","name":"compact","phase":"started"}`},
		{"compact bad phase", `{"type":"event","name":"compact","phase":"halfway","trigger":"auto"}`},
		{"compact bad trigger", `{"type":"event","name":"compact","phase":"started","trigger":"cron"}`},
		{"compact error on success", `{"type":"event","name":"compact","phase":"completed","trigger":"auto","error":"x"}`},
		{"compact with tool", `{"type":"event","name":"compact","phase":"started","trigger":"auto","tool":"Bash"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseReport([]byte(tc.body)); !errors.Is(err, ErrInvalidReport) {
				t.Fatalf("ParseReport(%s) error = %v, want ErrInvalidReport", tc.body, err)
			}
		})
	}
}

func TestHubAcceptsObserveEvents(t *testing.T) {
	body := `{"type":"event","name":"attention","state":"needs_input","kind":"question","tool":"AskUserQuestion"}`
	r, err := ParseReport([]byte(body))
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	ev := recordedEvent(t, r)
	want := &AttentionReport{State: AttentionNeedsInput, Kind: AttentionQuestion, Tool: "AskUserQuestion"}
	if ev.Name != EventAttention || !reflect.DeepEqual(ev.Attention, want) {
		t.Fatalf("event = %+v, want attention %+v", ev, want)
	}
	if _, _, ok := HookPayload(ev); ok {
		t.Fatal("observe events must not become dispatch hook reports")
	}
}

// recordedEvent applies r through a hub and returns the one event its
// subscriber saw.
func recordedEvent(t *testing.T, r Report) Event {
	t.Helper()
	var got []Event
	h := New(Options{Subscriber: SubscriberFunc(func(ev Event) { got = append(got, ev) })})
	t.Cleanup(h.Close)
	if _, err := h.Open("worker", "launch-1"); err != nil {
		t.Fatal(err)
	}
	if err := h.Apply("worker", "launch-1", r); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("events = %+v, want one", got)
	}
	return got[0]
}
