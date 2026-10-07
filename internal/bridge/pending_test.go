package bridge

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestParseReportTurnPending(t *testing.T) {
	body := `{"type":"event","name":"turn.complete","event_id":"turn.complete:t1","message":"I'll wait",` +
		`"pending":{"tasks":{"shell":1,"monitor":1},"wakeups":2}}`
	got, err := ParseReport([]byte(body))
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	if got.Pending == nil || got.Pending.Tasks["shell"] != 1 || got.Pending.Tasks["monitor"] != 1 || got.Pending.Wakeups != 2 {
		t.Fatalf("Pending = %+v", got.Pending)
	}
}

func TestParseReportWithoutPending(t *testing.T) {
	got, err := ParseReport([]byte(`{"type":"event","name":"turn.complete"}`))
	if err != nil || got.Pending != nil {
		t.Fatalf("Pending = %+v err=%v, want nil", got.Pending, err)
	}
}

func TestParseReportRejectsBadPending(t *testing.T) {
	cases := map[string]string{
		"on turn.start":    `{"type":"event","name":"turn.start","pending":{"tasks":{},"wakeups":0}}`,
		"not an object":    `{"type":"event","name":"turn.complete","pending":[1]}`,
		"null":             `{"type":"event","name":"turn.complete","pending":null}`,
		"negative count":   `{"type":"event","name":"turn.complete","pending":{"tasks":{"shell":-1}}}`,
		"huge count":       `{"type":"event","name":"turn.complete","pending":{"tasks":{"shell":100000}}}`,
		"negative wakeups": `{"type":"event","name":"turn.complete","pending":{"wakeups":-1}}`,
		"unknown key":      `{"type":"event","name":"turn.complete","pending":{"crons":1}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseReport([]byte(body)); !errors.Is(err, ErrInvalidReport) {
				t.Fatalf("ParseReport(%s) err = %v, want ErrInvalidReport", body, err)
			}
		})
	}
}

// The mod's pending work reaches the dispatcher in the Stop hook's own
// shape: background_tasks and session_crons entries.
func TestHookPayloadCarriesPendingWorkInStopShape(t *testing.T) {
	ev := Event{Agent: agentA, Name: EventTurnComplete, SessionID: "s-1", Message: "wait",
		Pending: &PendingWork{Tasks: map[string]int{"shell": 2}, Wakeups: 1}}
	_, raw, ok := HookPayload(ev)
	if !ok {
		t.Fatal("HookPayload not ok")
	}
	var got struct {
		Tasks []map[string]string `json:"background_tasks"`
		Crons []map[string]any    `json:"session_crons"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tasks) != 2 || got.Tasks[0]["type"] != "shell" || got.Tasks[0]["status"] != "running" || len(got.Crons) != 1 {
		t.Fatalf("payload = %s", raw)
	}
}

func TestParseReportPendingRejectsNullCounts(t *testing.T) {
	for name, body := range map[string]string{
		"null count":   `{"type":"event","name":"turn.complete","pending":{"tasks":{"constructor":null}}}`,
		"null wakeups": `{"type":"event","name":"turn.complete","pending":{"wakeups":null}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseReport([]byte(body)); !errors.Is(err, ErrInvalidReport) {
				t.Fatalf("err = %v, want ErrInvalidReport", err)
			}
		})
	}
}

// A type name the daemon would not store still counts, as "other": dropping
// it would finish a turn that still has work in flight.
func TestParseReportPendingCountsUnknownTypesAsOther(t *testing.T) {
	got, err := ParseReport([]byte(`{"type":"event","name":"turn.complete","pending":{"tasks":{"she ll\n":2,"x/y":1,"shell":1}}}`))
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	if got.Pending == nil || got.Pending.Tasks["other"] != 3 || got.Pending.Tasks["shell"] != 1 || len(got.Pending.Tasks) != 2 {
		t.Fatalf("Pending = %+v", got.Pending)
	}
}
