package consult

import (
	"math"
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

func TestRenderRosterUsageSuffixPreservesUnknownAndZero(t *testing.T) {
	now := time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)
	tools, input, output := 0, int64(0), int64(0)
	recs := []Record{{ID: "d-1", Kind: "dispatch", Name: "zero", Status: StatusRunning, StartedAt: now, ToolCalls: &tools, InputTokens: &input, OutputTokens: &output}, {ID: "d-2", Kind: "dispatch", Name: "tools", Status: StatusRunning, StartedAt: now.Add(time.Second), ToolCalls: &tools}}
	got := RenderRoster(recs, now)
	if !strings.Contains(got, "0 tools · 0.0k tokens") || !strings.Contains(got, "tools 0:00 · 0 tools") {
		t.Fatalf("roster usage = %q", got)
	}
}

func TestRosterUsagePluralizesTools(t *testing.T) {
	for _, tc := range []struct {
		tools int
		want  string
	}{
		{0, "0 tools"},
		{1, "1 tool"},
		{2, "2 tools"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			tools := tc.tools
			if got := rosterUsage(Record{ToolCalls: &tools}); got != " · "+tc.want {
				t.Fatalf("rosterUsage() = %q, want %q", got, " · "+tc.want)
			}
		})
	}
}

func TestHasRosterEligibleRecord(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		records []Record
		want    bool
	}{
		{"none", nil, false},
		{"non-dispatch", []Record{{Kind: "agent", Status: StatusRunning}}, false},
		{"running dispatch", []Record{{Kind: "dispatch", Status: StatusRunning}}, true},
		{"recent terminal dispatch", []Record{{Kind: "dispatch", Status: StatusDone, EndedAt: now.Add(-viewerGraceAfterEnd + time.Second)}}, true},
		{"expired terminal dispatch", []Record{{Kind: "dispatch", Status: StatusDone, EndedAt: now.Add(-viewerGraceAfterEnd)}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasRosterEligibleRecord(tc.records, now); got != tc.want {
				t.Fatalf("HasRosterEligibleRecord() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReleasedExcludedFromRoster(t *testing.T) {
	now := time.Now()
	rec := Record{ID: "d-x", Kind: "dispatch", Name: "released", Status: StatusReleased, StartedAt: now}
	if got := RenderRoster([]Record{rec}, now); strings.Contains(got, "released") {
		t.Fatalf("roster=%q", got)
	}
	if HasRosterEligibleRecord([]Record{rec}, now) {
		t.Fatal("released record is eligible")
	}
}

func TestRenderRosterMarksPartialUsageWithoutOverflow(t *testing.T) {
	now := time.Now()
	max := int64(math.MaxInt64)
	one := int64(1)
	got := RenderRoster([]Record{
		{ID: "a", Kind: "dispatch", Name: "partial", Status: StatusRunning, StartedAt: now, UsageIncomplete: true},
		{ID: "b", Kind: "dispatch", Name: "tokens", Status: StatusRunning, StartedAt: now.Add(time.Second), InputTokens: &max, OutputTokens: &one, UsageIncomplete: true},
	}, now)
	if !strings.Contains(got, "partial 0:00 · partial") || !strings.Contains(got, "9223372036854776.0k~ tokens") {
		t.Fatalf("roster=%q", got)
	}
}
