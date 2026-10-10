package consult

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

// claudeStop is a Stop hook report in the shape Claude Code posts it:
// pending work rides along as background_tasks and session_crons, either
// of which an older claude leaves out.
func claudeStop(t *testing.T, eventID, message string, extra map[string]any) HookReport {
	t.Helper()
	payload := map[string]any{"hook_event_name": "Stop", "stop_hook_active": false, "session_id": "s-1"}
	if message != "" {
		payload["last_assistant_message"] = message
	}
	for k, v := range extra {
		payload[k] = v
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return HookReport{EventID: eventID, Payload: b}
}

// A run_in_background Bash plus a Monitor, as Claude Code lists them.
var shellAndMonitor = map[string]any{
	"background_tasks": []any{
		map[string]any{"id": "b1", "type": "shell", "status": "running", "description": "go build ./...", "command": "go build ./..."},
		map[string]any{"id": "m1", "type": "monitor", "status": "running", "description": "watch build", "server": "x", "tool": "y"},
	},
	"session_crons": []any{},
}

const taskNotification = "<task-notification>\n<task-id>b1</task-id>\n<status>completed</status>\n</task-notification>"

func TestInteractiveStopWithBackgroundTasksWaitsInsteadOfFinishing(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	now = now.Add(time.Second)
	if err := d.Report(id, claudeStop(t, "stop-1", "I'll wait for the build.", shellAndMonitor)); err != nil {
		t.Fatal(err)
	}
	rec, _ := d.Get(id)
	if rec.Status != StatusWaiting || len(rec.Turns) != 1 || rec.Turns[0].Outcome != "" {
		t.Fatalf("stop with pending work: record=%+v", rec)
	}
	if rec.PendingWork == nil || rec.PendingWork.Tasks["shell"] != 1 || rec.PendingWork.Tasks["monitor"] != 1 || rec.PendingWork.Wakeups != 0 {
		t.Fatalf("pending work=%+v", rec.PendingWork)
	}
	if got := rec.PendingWork.Summary(); got != "1 shell · 1 monitor" {
		t.Fatalf("summary=%q", got)
	}
	entry := d.Wait(context.Background(), []string{id + "#1"}, 20*time.Millisecond)[0]
	if entry.Outcome != "" || entry.Status != StatusWaiting || entry.Pending != "1 shell · 1 monitor" {
		t.Fatalf("wait resolved on a waiting run: %+v", entry)
	}
}

// The task-notification Claude injects once the shell finishes continues
// the same logical turn: its Stop with nothing pending finishes the turn the
// orchestrator is waiting on, with that final message.
func TestInteractiveWaitingTurnFinishesAfterNotificationContinuation(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	done := make(chan []Entry, 1)
	go func() { done <- d.Wait(context.Background(), []string{id + "#1"}, time.Minute) }()
	if err := d.Report(id, claudeStop(t, "stop-1", "I'll wait for the build.", shellAndMonitor)); err != nil {
		t.Fatal(err)
	}
	advanceClock(&now, time.Minute)
	if err := d.Report(id, claudeHook(t, "submit-2", "UserPromptSubmit", taskNotification)); err != nil {
		t.Fatal(err)
	}
	rec, _ := d.Get(id)
	if rec.Status != StatusRunning || rec.PendingWork != nil || len(rec.Turns) != 1 || rec.Steered {
		t.Fatalf("after notification: record=%+v", rec)
	}
	advanceClock(&now, time.Second)
	if err := d.Report(id, claudeStop(t, "stop-2", "Build passed.", map[string]any{"background_tasks": []any{}, "session_crons": []any{}})); err != nil {
		t.Fatal(err)
	}
	select {
	case entries := <-done:
		if len(entries) != 1 || entries[0].Outcome != TurnFinished || entries[0].Text != "I'll wait for the build.\n\nBuild passed." || entries[0].Status != StatusIdle {
			t.Fatalf("entries=%+v", entries)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not resolve once the continuation stopped with nothing pending")
	}
	rec, _ = d.Get(id)
	if len(rec.Turns) != 1 || rec.Turns[0].TurnID != id+"#1" {
		t.Fatalf("continuation opened another turn: %+v", rec.Turns)
	}
}

func TestInteractiveStopWithSessionCronWaits(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	crons := map[string]any{"session_crons": []any{
		map[string]any{"id": "c1", "schedule": "5 12 7 10 *", "recurring": false, "prompt": "check CI"},
		map[string]any{"id": "c2", "schedule": "*/5 * * * *", "recurring": true, "prompt": "poll"},
	}}
	if err := d.Report(id, claudeStop(t, "stop-1", "Scheduled a check.", crons)); err != nil {
		t.Fatal(err)
	}
	rec, _ := d.Get(id)
	if rec.Status != StatusWaiting || rec.PendingWork == nil || rec.PendingWork.Wakeups != 2 || rec.PendingWork.Summary() != "2 wakeups" {
		t.Fatalf("record=%+v pending=%+v", rec, rec.PendingWork)
	}
}

func TestInteractiveStopWithoutPendingWorkFinishes(t *testing.T) {
	for name, extra := range map[string]map[string]any{
		"absent fields (older claude)": nil,
		"empty lists":                  {"background_tasks": []any{}, "session_crons": []any{}},
		"only settled tasks":           {"background_tasks": []any{map[string]any{"id": "b1", "type": "shell", "status": "completed"}}},
		"malformed":                    {"background_tasks": "nope", "session_crons": 3},
	} {
		t.Run(name, func(t *testing.T) {
			now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
			d, _, id := startClaudeInteractive(t, &now)
			if err := d.Report(id, claudeStop(t, "stop-1", "done", extra)); err != nil {
				t.Fatal(err)
			}
			rec, _ := d.Get(id)
			if rec.Status != StatusIdle || rec.PendingWork != nil || rec.Turns[0].Outcome != TurnFinished || rec.Turns[0].Text != "done" {
				t.Fatalf("record=%+v", rec)
			}
		})
	}
}

// An interrupted turn ends whatever is still running in the background.
func TestInteractiveInterruptWithBackgroundTasksStillCloses(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	payload := map[string]any{"hook_event_name": "Interrupt"}
	for k, v := range shellAndMonitor {
		payload[k] = v
	}
	b, _ := json.Marshal(payload)
	if err := d.Report(id, HookReport{EventID: "int-1", Payload: b}); err != nil {
		t.Fatal(err)
	}
	rec, _ := d.Get(id)
	if rec.Status != StatusIdle || rec.Turns[0].Outcome != TurnInterrupted {
		t.Fatalf("record=%+v", rec)
	}
}

func TestInteractiveWaitingRunIsNotStalledOrIdleClosed(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	d, rt, id := startClaudeInteractive(t, &now)
	if err := d.Report(id, claudeStop(t, "stop-1", "waiting", shellAndMonitor)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(idleCloseAfter + stalledAfter)
	d.Sweep(now)
	rec, _ := d.Get(id)
	if rec.Status != StatusWaiting {
		t.Fatalf("sweep moved a waiting run to %s", rec.Status)
	}
	if got := rt.killCount(); got != 0 {
		t.Fatalf("waiting run's pane killed: %d", got)
	}
	entry := d.Wait(context.Background(), []string{id + "#1"}, time.Millisecond)[0]
	if entry.Stalled {
		t.Fatalf("waiting run reported stalled: %+v", entry)
	}
	if isStalled(rec, now) {
		t.Fatal("bridge state reports a waiting run stalled")
	}
}

func TestInteractiveSendRejectedWhileWaiting(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	if err := d.Report(id, claudeStop(t, "stop-1", "waiting", shellAndMonitor)); err != nil {
		t.Fatal(err)
	}
	_, err := d.Send(context.Background(), id, "next")
	if err == nil || !strings.Contains(err.Error(), "waiting on background work (1 shell · 1 monitor)") {
		t.Fatalf("Send while waiting err=%v", err)
	}
}

func TestInteractiveCancelWhileWaiting(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	if err := d.Report(id, claudeStop(t, "stop-1", "waiting", shellAndMonitor)); err != nil {
		t.Fatal(err)
	}
	rec, err := d.Cancel(id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != StatusCanceled && rec.Status != StatusSettling {
		t.Fatalf("cancel of a waiting run: status=%s", rec.Status)
	}
}

func TestBridgeDispatchStateCarriesPendingSummary(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	rec := Record{ID: "d-1", Kind: "dispatch", Status: StatusWaiting, CallerBridgeKey: "k", StartedAt: now,
		PendingWork: &PendingWork{Tasks: map[string]int{"shell": 2, "monitor": 1}, Wakeups: 1}}
	states := BridgeDispatchStates([]Record{rec}, "k", now)
	if len(states) != 1 || states[0].Status != "waiting" || states[0].Pending != "2 shells · 1 monitor · 1 wakeup" {
		t.Fatalf("states=%+v", states)
	}
}

func TestObservedDispatchCarriesPendingSummary(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	rec := Record{ID: "d-1", Kind: "dispatch", Status: StatusWaiting, StartedAt: now,
		PendingWork: &PendingWork{Tasks: map[string]int{"shell": 1}}}
	if got := observedDispatch(rec, now); got.Status != "waiting" || got.Pending != "1 shell" {
		t.Fatalf("observed=%+v", got)
	}
}

// A bridged dispatch reports its Stop as the mod's turn.complete; the
// pending work the mod read off Claude's Stop hook rides along.
func TestInteractiveBridgedTurnCompleteWithPendingWorkWaits(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	eventID, payload, ok := bridge.HookPayload(bridge.Event{Name: bridge.EventTurnComplete, EventID: "turn.complete:t1", Message: "waiting on CI",
		Pending: &bridge.PendingWork{Tasks: map[string]int{"shell": 1}, Wakeups: 1}})
	if !ok {
		t.Fatal("HookPayload not ok")
	}
	if err := d.Report(id, HookReport{EventID: eventID, Payload: payload}); err != nil {
		t.Fatal(err)
	}
	rec, _ := d.Get(id)
	if rec.Status != StatusWaiting || rec.Turns[0].Outcome != "" || rec.PendingWork.Summary() != "1 shell · 1 wakeup" {
		t.Fatalf("record=%+v pending=%+v", rec, rec.PendingWork)
	}
}

// A waiting run's pending work can end with nothing to wake the session (a
// Monitor that expired, a shell killed outright): after waitingStalledAfter
// with no hook activity it reads stalled, like a quiet busy run, but is not
// finished on its own.
func TestInteractiveWaitingRunReadsStalledAfterLongQuiet(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	if err := d.Report(id, claudeStop(t, "stop-1", "waiting", shellAndMonitor)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(waitingStalledAfter - time.Second)
	if entry := d.Wait(context.Background(), []string{id + "#1"}, time.Millisecond)[0]; entry.Stalled {
		t.Fatalf("stalled before waitingStalledAfter: %+v", entry)
	}
	now = now.Add(time.Second)
	d.Sweep(now)
	entry := d.Wait(context.Background(), []string{id + "#1"}, time.Millisecond)[0]
	if !entry.Stalled || entry.Status != StatusWaiting || entry.Outcome != "" {
		t.Fatalf("after waitingStalledAfter: %+v", entry)
	}
	rec, _ := d.Get(id)
	if !isStalled(rec, now) || !observedDispatch(rec, now).Stalled {
		t.Fatal("bridge/observe state does not flag a long-quiet waiting run stalled")
	}
}

// A pane that dies while its run waits ends the open turn as lost, as it
// does a busy run's.
func TestInteractiveWaitingRunPaneDeathLosesTurn(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	d, rt, id := startClaudeInteractive(t, &now)
	if err := d.Report(id, claudeStop(t, "stop-1", "waiting", shellAndMonitor)); err != nil {
		t.Fatal(err)
	}
	rt.setAlive(false)
	d.Sweep(now)
	now = now.Add(finalReportGrace)
	d.Sweep(now)
	rec, _ := d.Get(id)
	if !rec.Status.Terminal() || rec.Turns[0].Outcome != TurnLost || rec.PendingWork != nil {
		t.Fatalf("dead pane while waiting: status=%s turns=%+v pending=%+v", rec.Status, rec.Turns, rec.PendingWork)
	}
}

// startClaudeArmed launches a claude dispatch whose opening is armed but
// whose UserPromptSubmit has not arrived yet.
func startClaudeArmed(t *testing.T, now *time.Time) (*Dispatcher, string) {
	t.Helper()
	d := NewDispatcher(newFakeRecorder())
	d.now = func() time.Time { return *now }
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	waitForArmed(t, d, started.ID)
	return d, started.ID
}

// A Stop can overtake its turn's UserPromptSubmit: it confirms the armed
// opening's delivery, and then pending work keeps that turn open.
func TestInteractiveStopBeforeOpeningSubmitWithPendingWorkWaits(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	d, id := startClaudeArmed(t, &now)
	if err := d.Report(id, claudeStop(t, "stop-1", "waiting", shellAndMonitor)); err != nil {
		t.Fatal(err)
	}
	rec, _ := d.Get(id)
	if rec.Status != StatusWaiting || len(rec.Turns) != 1 || !rec.Turns[0].Delivered || rec.Turns[0].Outcome != "" {
		t.Fatalf("stop before submit: status=%s turns=%+v", rec.Status, rec.Turns)
	}
}

func TestInteractiveStopBeforeOpeningSubmitConfirmsDelivery(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	d, id := startClaudeArmed(t, &now)
	if err := d.Report(id, claudeStop(t, "stop-1", "done", nil)); err != nil {
		t.Fatal(err)
	}
	rec, _ := d.Get(id)
	if rec.Status != StatusIdle || !rec.Turns[0].Delivered || rec.Turns[0].Outcome != TurnFinished || rec.Turns[0].Text != "done" {
		t.Fatalf("stop before submit: status=%s turns=%+v", rec.Status, rec.Turns)
	}
}

// An opening can run far longer than lateAckWindow with its submit never
// seen; its Stop with pending work must still keep it open.
func TestInteractiveStopLongAfterArmedOpeningWithPendingWorkWaits(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	d, id := startClaudeArmed(t, &now)
	now = now.Add(lateAckWindow + time.Hour)
	if err := d.Report(id, claudeStop(t, "stop-1", "waiting", shellAndMonitor)); err != nil {
		t.Fatal(err)
	}
	rec, _ := d.Get(id)
	if rec.Status != StatusWaiting || !rec.Turns[0].Delivered || rec.Turns[0].Outcome != "" {
		t.Fatalf("status=%s turns=%+v", rec.Status, rec.Turns)
	}
}

const longSummary = "Implemented the change.\nAll tests pass.\nCommit abc123."

var nothingPending = map[string]any{"background_tasks": []any{}, "session_crons": []any{}}

// A turn that ends with background work pending and later closes on a terse
// wake reply keeps the summary it wrote before pausing.
func TestInteractiveWaitingStopTextSurvivesIdLessWake(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	reportAll(t, d, id,
		claudeStop(t, "stop-1", longSummary, shellAndMonitor),
		claudeHook(t, "submit-2", "UserPromptSubmit", taskNotification),
		claudeStop(t, "stop-2", "Killed.", nothingPending))
	rec, _ := d.Get(id)
	if got, want := rec.Turns[0].Text, longSummary+"\n\nKilled."; got != want || rec.Turns[0].Outcome != TurnFinished {
		t.Fatalf("turn=%+v want text %q", rec.Turns[0], want)
	}
}

func TestPromptIDWaitingStopTextSurvivesWake(t *testing.T) {
	d, _, id, _ := startArmedClaude(t)
	reportAll(t, d, id,
		idSubmit(t, "u1", "a", openingText), idStopWaiting(t, "s1", "a", longSummary),
		idSubmit(t, "u2", "w", taskNotification), idStopWaiting(t, "s2", "w", "Still waiting."),
		idStop(t, "s3", "w", "Killed."))
	rec := idleRecord(t, d, id)
	if got, want := rec.Turns[0].Text, longSummary+"\n\nStill waiting.\n\nKilled."; got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

// Empty pieces and a piece repeating the previous one add nothing.
func TestPromptIDWaitingStopTextSkipsEmptyAndRepeats(t *testing.T) {
	d, _, id, _ := startArmedClaude(t)
	reportAll(t, d, id,
		idSubmit(t, "u1", "a", openingText), idStopWaiting(t, "s1", "a", longSummary),
		idSubmit(t, "u2", "w", taskNotification), idStopWaiting(t, "s2", "w", ""),
		idStop(t, "s3", "w", longSummary))
	rec := idleRecord(t, d, id)
	if got := rec.Turns[0].Text; got != longSummary {
		t.Fatalf("text = %q, want %q", got, longSummary)
	}
}

// An interrupt that closes a waiting turn with no words of its own still
// delivers what the turn said before it paused.
func TestInteractiveWaitingStopTextSurvivesInterrupt(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	reportAll(t, d, id, claudeStop(t, "stop-1", longSummary, shellAndMonitor),
		HookReport{EventID: "int-1", Payload: []byte(`{"hook_event_name":"Interrupt"}`)})
	rec, _ := d.Get(id)
	if rec.Turns[0].Outcome != TurnInterrupted || rec.Turns[0].Text != longSummary {
		t.Fatalf("turn=%+v", rec.Turns[0])
	}
}

func TestInteractiveWaitingStopTextSurvivesSettlement(t *testing.T) {
	now := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	d, _, id := startClaudeInteractive(t, &now)
	reportAll(t, d, id, claudeStop(t, "stop-1", longSummary, shellAndMonitor))
	d.mu.Lock()
	s := d.runs[id]
	d.finishInteractiveLocked(s, StatusClosed)
	d.mu.Unlock()
	rec, _ := d.Get(id)
	if rec.Turns[0].Text != longSummary {
		t.Fatalf("turn=%+v", rec.Turns[0])
	}
}

func TestTurnWaitTextRoundTripsThroughJSON(t *testing.T) {
	in := Turn{TurnID: "d-1#1", WaitText: []string{"a", "b"}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Turn
	if err := json.Unmarshal(b, &out); err != nil || len(out.WaitText) != 2 || out.WaitText[1] != "b" {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}
