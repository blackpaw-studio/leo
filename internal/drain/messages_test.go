package drain

import (
	"strings"
	"testing"
	"time"
)

func TestPauseTextNamesTimeoutInMinutes(t *testing.T) {
	got := PauseText(10 * time.Minute)
	if !strings.Contains(got, "restarts in 10 minutes") {
		t.Fatalf("PauseText(10m) = %q, want it to contain %q", got, "restarts in 10 minutes")
	}
	want := "[leo] URGENT: the leo daemon restarts in 10 minutes and this session may be killed. Stop now: do not start new steps, tool calls, or dispatches. Finish or abandon the current step, save or commit work in progress, write a short note of where you are and what's next, then end your turn immediately. Anything unsaved when the restart happens may be lost. You'll get a resume message afterward."
	if got != want {
		t.Fatalf("PauseText(10m) = %q, want the spec text %q", got, want)
	}
}

func TestPauseTextRoundsMinutesUpWithFloorOfOne(t *testing.T) {
	for _, tc := range []struct {
		timeout time.Duration
		want    string
	}{
		{90 * time.Second, "restarts in 2 minutes"},
		{20 * time.Second, "restarts in 1 minutes"},
		{0, "restarts in 1 minutes"},
	} {
		if got := PauseText(tc.timeout); !strings.Contains(got, tc.want) {
			t.Errorf("PauseText(%v) = %q, want it to contain %q", tc.timeout, got, tc.want)
		}
	}
}

func TestCancelAndResumeTextsMatchSpec(t *testing.T) {
	if CancelText != "[leo] Restart cancelled. Resume your work." {
		t.Errorf("CancelText = %q", CancelText)
	}
	if ResumeText != "[leo] Daemon restarted. Resume where you left off." {
		t.Errorf("ResumeText = %q", ResumeText)
	}
}
