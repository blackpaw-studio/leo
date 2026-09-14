package consult

import (
	"strings"
	"testing"
	"time"
)

func TestRenderRosterStatusesOrderAndFrozenTime(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	runningSince := now.Add(-83 * time.Second)
	records := []Record{
		{ID: "d-2", Kind: "dispatch", Name: "idle", Status: StatusIdle, StartedAt: now.Add(-3 * time.Minute), ActiveSeconds: 41},
		{ID: "d-1", Kind: "dispatch", Name: "run", Status: StatusRunning, StartedAt: now.Add(-4 * time.Minute), RunningSince: &runningSince},
		{ID: "d-3", Kind: "dispatch", Name: "settle", Status: StatusSettling, StartedAt: now.Add(-2 * time.Minute), ActiveSeconds: 9},
		{ID: "d-4", Kind: "dispatch", Name: "queued", Status: StatusQueued, StartedAt: now.Add(-time.Minute)},
		{ID: "d-5", Kind: "dispatch", Name: "done", Status: StatusDone, StartedAt: now.Add(-30 * time.Second), ActiveSeconds: 3700, EndedAt: now},
		{ID: "d-6", Kind: "dispatch", Name: "bad", Status: StatusTimeout, StartedAt: now.Add(-20 * time.Second), ActiveSeconds: 2, EndedAt: now},
	}

	got := RenderRoster(records, now)
	for _, want := range []string{"⟳ run 1:23", "⏸ idle 0:41", "⏸ settle 0:09", "… queued 0:00", "✓ done 1:01:40", "✗ bad 0:02"} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderRoster missing %q in %q", want, got)
		}
	}
	if strings.Index(got, "run") > strings.Index(got, "idle") {
		t.Fatalf("RenderRoster order = %q", got)
	}
}

func TestRenderRosterFiltersEscapesAndDoesNotMutate(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	records := []Record{
		{ID: "d-a", Kind: "consult", Name: "omit", Status: StatusRunning, StartedAt: now},
		{ID: "d-b", Kind: "dispatch", Name: "12345678901234567#[x]", Status: StatusDone, StartedAt: now, EndedAt: now.Add(-viewerGraceAfterEnd)},
		{ID: "d-c", Kind: "dispatch", Template: "a b:c.d#[x]", Status: StatusIdle, StartedAt: now},
	}
	original := records[2].Template
	got := RenderRoster(records, now)
	if strings.Contains(got, "omit") || strings.Contains(got, "1234567890123456") {
		t.Fatalf("RenderRoster retained filtered records: %q", got)
	}
	if !strings.Contains(got, "a-b-c-d##[x]") {
		t.Fatalf("RenderRoster did not sanitize/escape label: %q", got)
	}
	if records[2].Template != original {
		t.Fatal("RenderRoster mutated its input")
	}
}

func TestRenderRosterDeterministicIDTieBreak(t *testing.T) {
	now := time.Now()
	records := []Record{
		{ID: "d-b", Kind: "dispatch", Name: "second", Status: StatusQueued, StartedAt: now},
		{ID: "d-a", Kind: "dispatch", Name: "first", Status: StatusQueued, StartedAt: now},
	}
	got := RenderRoster(records, now)
	if strings.Index(got, "first") > strings.Index(got, "second") {
		t.Fatalf("RenderRoster tie order = %q", got)
	}
}
