package bridge

import (
	"errors"
	"testing"
)

// An effort event carries the level a main-loop step asked for.
func TestParseReportEffortEvent(t *testing.T) {
	r, err := ParseReport([]byte(`{"type":"event","name":"effort","level":"xhigh"}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != EventEffort || r.Effort != "xhigh" {
		t.Fatalf("report = %+v, want an effort event at xhigh", r)
	}
}

func TestParseReportEffortEventRejectsBadLevels(t *testing.T) {
	for _, body := range []string{
		`{"type":"event","name":"effort"}`,
		`{"type":"event","name":"effort","level":"turbo"}`,
		`{"type":"event","name":"effort","level":3}`,
		`{"type":"event","name":"turn.start","level":"high"}`,
	} {
		if _, err := ParseReport([]byte(body)); !errors.Is(err, ErrInvalidReport) {
			t.Errorf("%s: err = %v, want ErrInvalidReport", body, err)
		}
	}
}

// An effort event has no shell-hook counterpart.
func TestHookPayloadSkipsEffortEvents(t *testing.T) {
	if _, _, ok := HookPayload(Event{Name: EventEffort, Effort: "high"}); ok {
		t.Fatal("an effort event became a hook report")
	}
}
