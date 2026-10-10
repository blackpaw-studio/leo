package consult

import (
	"encoding/json"
	"testing"
	"time"
)

// toolHook is a PreToolUse / PostToolUse / PostToolUseFailure report in the
// shape Claude Code posts it.
func toolHook(t *testing.T, eventID, event, toolUseID string) HookReport {
	t.Helper()
	b, err := json.Marshal(map[string]any{"hook_event_name": event, "session_id": "s-1", "tool_name": "Bash", "tool_use_id": toolUseID})
	if err != nil {
		t.Fatal(err)
	}
	return HookReport{EventID: eventID, Payload: b}
}

func stalledAt(t *testing.T, d *Dispatcher, id string, now time.Time) bool {
	t.Helper()
	return isStalled(idleRecord(t, d, id), now)
}

// A foreground tool call (a 15-minute test run) sends no hooks until it
// ends: an open tool holds the long threshold instead of the 10-minute one.
func TestInteractiveOpenToolSuppressesStallPastStalledAfter(t *testing.T) {
	now := time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	reportAll(t, d, id, toolHook(t, "pre-1", "PreToolUse", "tool-1"))
	opened := now
	if stalledAt(t, d, id, opened.Add(stalledAfter+time.Minute)) {
		t.Fatal("run stalled while a tool call is open")
	}
	if stalledAt(t, d, id, opened.Add(waitingStalledAfter-time.Second)) {
		t.Fatal("run stalled before waitingStalledAfter with a tool open")
	}
}

// A tool that settled is no longer in flight: the run is quiet like any other.
func TestInteractiveClosedToolDoesNotSuppressStall(t *testing.T) {
	for _, post := range []string{"PostToolUse", "PostToolUseFailure"} {
		t.Run(post, func(t *testing.T) {
			now := time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC)
			d, _, id := startClaudeInteractive(t, &now)
			reportAll(t, d, id, toolHook(t, "pre-1", "PreToolUse", "tool-1"))
			now = now.Add(time.Minute)
			reportAll(t, d, id, toolHook(t, "post-1", post, "tool-1"))
			settled := now
			if stalledAt(t, d, id, settled.Add(stalledAfter-time.Second)) {
				t.Fatal("stalled before stalledAfter")
			}
			if !stalledAt(t, d, id, settled.Add(stalledAfter)) {
				t.Fatal("not stalled stalledAfter after the tool closed")
			}
		})
	}
}

// A tool whose PostToolUse never comes (Esc, a killed shell) must not hold
// the run open forever: the cap is waitingStalledAfter from the last hook.
func TestInteractiveOrphanedOpenToolStallsAfterWaitingStalledAfter(t *testing.T) {
	now := time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	reportAll(t, d, id, toolHook(t, "pre-1", "PreToolUse", "tool-1"))
	if !stalledAt(t, d, id, now.Add(waitingStalledAfter)) {
		t.Fatal("orphaned open tool never stalls")
	}
}

// Parallel and subagent tool calls overlap: the run is in flight until the
// last one settles, so this is a set keyed by tool_use_id, not a counter.
func TestInteractiveParallelToolsStayOpenUntilAllSettle(t *testing.T) {
	now := time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	reportAll(t, d, id,
		toolHook(t, "pre-a", "PreToolUse", "a"),
		toolHook(t, "pre-b", "PreToolUse", "b"),
		toolHook(t, "post-a", "PostToolUse", "a"),
		// A repeated Post for a settled id must not close b.
		toolHook(t, "post-a2", "PostToolUse", "a"),
	)
	if stalledAt(t, d, id, now.Add(stalledAfter+time.Minute)) {
		t.Fatal("stalled while tool b is still open")
	}
	reportAll(t, d, id, toolHook(t, "post-b", "PostToolUse", "b"))
	if !stalledAt(t, d, id, now.Add(stalledAfter)) {
		t.Fatal("not stalled after every tool settled")
	}
}

// Every turn boundary drops tools still open: they belong to a turn that
// is over, so none may suppress the next turn's stall.
func TestInteractiveTurnBoundariesClearOpenTools(t *testing.T) {
	for _, tc := range []struct{ name, event string }{
		{"stop", "Stop"}, {"interrupt", "Interrupt"}, {"prompt submit", "UserPromptSubmit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC)
			d, _, id := startClaudeInteractive(t, &now)
			reportAll(t, d, id, toolHook(t, "pre-1", "PreToolUse", "tool-1"))
			reportAll(t, d, id, claudeHook(t, "boundary", tc.event, "typed by a human"))
			rec := idleRecord(t, d, id)
			if len(rec.OpenTools) != 0 {
				t.Fatalf("open tools after %s: %v", tc.event, rec.OpenTools)
			}
		})
	}
}

func TestInteractiveSessionEndClearsOpenTools(t *testing.T) {
	now := time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	reportAll(t, d, id, toolHook(t, "pre-1", "PreToolUse", "tool-1"))
	reportAll(t, d, id, claudeHook(t, "end", "SessionEnd", ""))
	rec := idleRecord(t, d, id)
	if len(rec.OpenTools) != 0 {
		t.Fatalf("open tools after SessionEnd: %v", rec.OpenTools)
	}
}

// Tool events are activity only: they never steer, close, or open a turn,
// change the status, or reach the result text.
func TestInteractiveToolEventsAreActivityOnly(t *testing.T) {
	now := time.Date(2026, time.October, 9, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	before := idleRecord(t, d, id)
	now = now.Add(time.Minute)
	reportAll(t, d, id,
		toolHook(t, "pre-1", "PreToolUse", "tool-1"),
		toolHook(t, "post-1", "PostToolUse", "tool-1"),
		toolHook(t, "pre-2", "PreToolUse", "tool-2"),
		toolHook(t, "fail-2", "PostToolUseFailure", "tool-2"),
	)
	after := idleRecord(t, d, id)
	if after.Status != before.Status || after.Steered || after.Text != before.Text || len(after.Turns) != len(before.Turns) {
		t.Fatalf("tool events changed the run: before=%+v after=%+v", before, after)
	}
	for i, turn := range after.Turns {
		if turn.Outcome != before.Turns[i].Outcome || turn.Text != before.Turns[i].Text {
			t.Fatalf("turn %d changed: %+v -> %+v", i, before.Turns[i], turn)
		}
	}
	if !after.HookActivity.Equal(now) {
		t.Fatalf("HookActivity = %v, want %v", after.HookActivity, now)
	}
}
