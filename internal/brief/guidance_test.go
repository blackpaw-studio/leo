package brief

import (
	"strings"
	"testing"
	"time"
)

func TestRestoredListsEveryFieldOfTheBrief(t *testing.T) {
	got := Restored(Brief{
		Goal: "ship it", Status: "testing", Decisions: []string{"atomic writes"}, Open: []string{"review", "docs"},
		UpdatedAt: time.Date(2026, 11, 25, 2, 0, 0, 0, time.UTC),
	}, "clear")
	for _, want := range []string{"after clear", "goal: ship it", "status: testing", "- atomic writes", "- review", "- docs", "leo_brief"} {
		if !strings.Contains(got, want) {
			t.Errorf("restored context lacks %q:\n%s", want, got)
		}
	}
}

func TestRestoredSkipsEmptyFields(t *testing.T) {
	got := Restored(Brief{Goal: "only goal"}, "compact")
	if strings.Contains(got, "decisions") || strings.Contains(got, "open") || strings.Contains(got, "status:") {
		t.Errorf("empty fields listed:\n%s", got)
	}
}

func TestNudgeDiffersForAMissingAndAStaleBrief(t *testing.T) {
	missing, stale := NudgeText(false), NudgeText(true)
	if !strings.Contains(missing, "missing") || !strings.Contains(stale, "stale") || missing == stale {
		t.Errorf("missing = %q, stale = %q", missing, stale)
	}
	if !strings.Contains(missing, "leo_brief") || !strings.Contains(stale, "leo_brief") {
		t.Error("a nudge must name the tool")
	}
}

func TestInstructionNamesTheToolAndTheLimits(t *testing.T) {
	for _, want := range []string{"leo_brief", "140", "goal", "status", "decisions", "open"} {
		if !strings.Contains(Instruction, want) {
			t.Errorf("instruction lacks %q", want)
		}
	}
}
