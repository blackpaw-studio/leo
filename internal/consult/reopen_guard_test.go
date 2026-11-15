package consult

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// sessionHook is a claude hook payload naming the session it came from.
func sessionHook(t *testing.T, eventID, event, prompt, sessionID string) HookReport {
	t.Helper()
	payload := map[string]string{"hook_event_name": event, "session_id": sessionID}
	if prompt != "" {
		payload["prompt"] = prompt
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return HookReport{EventID: eventID, Payload: b}
}

// reopened parks an isolated run, then sends it a follow-up that reopens
// it at once. The clock is the test's to advance.
func reopened(t *testing.T, now *time.Time) (*Dispatcher, *paneRuntime, *fakeNotificationDelivery, string, Record) {
	t.Helper()
	configDir := claudeConfigDir(t)
	d, rt, delivery, id := startParkableAt(t, t.TempDir(), true, "the answer", now)
	d.SweepNotifications(context.Background())
	parkedRec := requireParked(t, d, id, id+"#1")
	writeTranscript(t, configDir, parkedRec.Worktree, parkedRec.SessionID, nil)
	sent, err := d.SendWithConfig(context.Background(), testConfig(), id, "more")
	if err != nil || sent.TurnID != id+"#2" {
		t.Fatalf("reopen = %+v, %v", sent, err)
	}
	waitForArmed(t, d, id)
	return d, rt, delivery, id, parkedRec
}

func advance(now *time.Time, by time.Duration) {
	testClockMu.Lock()
	*now = now.Add(by)
	testClockMu.Unlock()
}

// requireRevertedToParked asserts a failed reopen left the run parked as it
// was, its worktree in place, no close notice sent, and turn #2 closed with
// outcome and text naming what happened.
func requireRevertedToParked(t *testing.T, d *Dispatcher, id string, before Record, outcome TurnOutcome, text string) Record {
	t.Helper()
	got, err := d.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !parked(got) || got.PaneID != "" || got.SessionID != before.SessionID {
		t.Fatalf("not parked again: status=%s retained=%v pane=%q session=%q", got.Status, got.Retained, got.PaneID, got.SessionID)
	}
	if _, ok := got.Notifications[closeNoticeKey(id)]; ok {
		t.Fatalf("a failed reopen sent a close notice: %+v", got.Notifications)
	}
	if before.Worktree != "" && (got.WorktreeState != WorktreePresent || !dirExists(before.Worktree)) {
		t.Fatalf("worktree state=%q exists=%v", got.WorktreeState, dirExists(before.Worktree))
	}
	turn := turnByID(got, id+"#2")
	if turn.Outcome != outcome || !strings.Contains(turn.Text, text) {
		t.Fatalf("turn #2 outcome=%q text=%q, want %q containing %q", turn.Outcome, turn.Text, outcome, text)
	}
	return got
}

func TestReopenFreshSessionDetectedRevertsToParked(t *testing.T) {
	now := time.Date(2026, time.November, 14, 12, 0, 0, 0, time.UTC)
	d, rt, _, id, before := reopened(t, &now)

	if err := d.Report(id, sessionHook(t, "submit-2", "UserPromptSubmit", "more", "s-2")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return status(d, id).PaneID == "" }) // the kill is a pane op

	got := requireRevertedToParked(t, d, id, before, TurnRejected, "could not resume session s-1 (harness started s-2); nothing was continued")
	if got.ResumeError != "could not resume session s-1 (harness started s-2)" {
		t.Fatalf("resume error = %q", got.ResumeError)
	}
	if len(rt.killedPanes()) != 2 {
		t.Fatalf("killed panes = %q, want the opening's and the reopen's", rt.killedPanes())
	}
	_, err := d.SendWithConfig(context.Background(), testConfig(), id, "again")
	if err == nil || !strings.Contains(err.Error(), "closed and is not resumable: could not resume session s-1") {
		t.Fatalf("send after a fresh session = %v", err)
	}
}

func TestReopenPaneExitBeforeConfirmRevertsToParked(t *testing.T) {
	now := time.Date(2026, time.November, 14, 12, 0, 0, 0, time.UTC)
	d, rt, _, id, before := reopened(t, &now)
	rt.setAlive(false) // claude exits: "No conversation found with session ID"

	d.Sweep(time.Time{})
	advance(&now, finalReportGrace)
	// The dead pane's kill is retried by the sweep until it lands.
	waitFor(t, func() bool { d.Sweep(time.Time{}); return status(d, id).PaneID == "" })

	got := requireRevertedToParked(t, d, id, before, TurnRejected, "nothing was continued")
	if got.ResumeError == "" || !strings.Contains(got.ResumeError, "exited before it resumed session s-1") {
		t.Fatalf("resume error = %q", got.ResumeError)
	}
}

func TestReopenLaunchErrorRevertsToParked(t *testing.T) {
	configDir := claudeConfigDir(t)
	d, rt, _, id := startParkable(t, t.TempDir(), true, "the answer")
	d.SweepNotifications(context.Background())
	before := requireParked(t, d, id, id+"#1")
	writeTranscript(t, configDir, before.Worktree, before.SessionID, nil)
	rt.mu.Lock()
	rt.launchErr = errors.New("tmux: no server running")
	rt.mu.Unlock()

	_, err := d.SendWithConfig(context.Background(), testConfig(), id, "more")

	if err == nil || !strings.Contains(err.Error(), "no server running") {
		t.Fatalf("send error = %v", err)
	}
	got := requireRevertedToParked(t, d, id, before, TurnRejected, "no server running")
	if got.ResumeError != "" {
		t.Fatalf("a launch failure made the run unresumable: %q", got.ResumeError)
	}
	rt.mu.Lock()
	rt.launchErr = nil
	rt.mu.Unlock()
	sent, err := d.SendWithConfig(context.Background(), testConfig(), id, "more, again")
	if err != nil || sent.TurnID != id+"#3" {
		t.Fatalf("retry = %+v, %v", sent, err)
	}
}

func TestCancelQueuedReopenRevertsToParked(t *testing.T) {
	d, rt, id, earlier, before := queuedReopen(t, t.TempDir())

	if _, err := d.Cancel(id); err != nil {
		t.Fatal(err)
	}

	got := requireRevertedToParked(t, d, id, before, TurnInterrupted, "nothing was continued")
	if got.ResumeError != "" {
		t.Fatalf("a cancel made the run unresumable: %q", got.ResumeError)
	}
	// The reopen's queued launch leaves the slot line once it sees the run
	// settled.
	waitFor(t, func() bool { return d.slots.Waiting() == 1 })
	d.slots.Release()
	waitFor(t, func() bool { return len(rt.launchRequests()) >= 2 })
	waitForArmed(t, d, earlier)
	for _, l := range rt.launchRequests() {
		if l.ID == id && l.ResumeSessionID != "" {
			t.Fatalf("a canceled reopen launched: %+v", l)
		}
	}
}

func TestRestartWithQueuedReopenRevertsToParked(t *testing.T) {
	stateDir := t.TempDir()
	_, _, id, _, before := queuedReopen(t, stateDir)
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.SetInteractiveRuntime(newPaneRuntime(false))

	markInterruptedAndReconcile(t, d)

	got := mustLoad(t, stateDir, id)
	if !parked(got) || got.ResumeError != "" || got.SessionID != before.SessionID {
		t.Fatalf("restart settled a queued reopen: status=%s retained=%v resume_error=%q", got.Status, got.Retained, got.ResumeError)
	}
	if _, ok := got.Notifications[closeNoticeKey(id)]; ok {
		t.Fatalf("restart sent a close notice for a queued reopen: %+v", got.Notifications)
	}
	turn := turnByID(got, id+"#2")
	if turn.Outcome != TurnInterrupted || !strings.Contains(turn.Text, "daemon restarted") {
		t.Fatalf("turn #2 outcome=%q text=%q", turn.Outcome, turn.Text)
	}
}

func TestConfirmedReopenIdleCloseIsTerminal(t *testing.T) {
	now := time.Date(2026, time.November, 14, 12, 0, 0, 0, time.UTC)
	d, _, delivery, id, _ := reopened(t, &now)
	delivery.ready = false // the result never reaches the caller, so nothing parks it

	if err := d.Report(id, sessionHook(t, "submit-2", "UserPromptSubmit", "more", "s-1")); err != nil {
		t.Fatal(err)
	}
	if err := d.Report(id, claudeStop(t, "stop-2", "second answer", nil)); err != nil {
		t.Fatal(err)
	}
	advance(&now, idleCloseAfter)
	d.Sweep(time.Time{})

	got, _ := d.Get(id)
	if got.Status != StatusClosed || got.Retained || parked(got) {
		t.Fatalf("idle close of a confirmed reopen: status=%s retained=%v", got.Status, got.Retained)
	}
	if _, ok := got.Notifications[closeNoticeKey(id)]; !ok {
		t.Fatal("an idle close of a confirmed reopen sent no close notice")
	}
}

func TestReopenTimeoutCountsFromTheReopen(t *testing.T) {
	configDir := claudeConfigDir(t)
	now := time.Date(2026, time.November, 14, 12, 0, 0, 0, time.UTC)
	stateDir := t.TempDir()
	rec := seedReopenable(t, stateDir, configDir, "d-tmo", func(r *Record) { r.Timeout = time.Hour })
	d, _, _ := reopenDispatcher(t, stateDir)
	d.now = sharedClock(&now) // well past StartedAt + Timeout

	if _, err := d.SendWithConfig(context.Background(), testConfig(), rec.ID, "more"); err != nil {
		t.Fatal(err)
	}
	d.Sweep(time.Time{})

	if got, _ := d.Get(rec.ID); got.Status.Terminal() || got.Status == StatusSettling {
		t.Fatalf("a reopen timed out on the original start: %s", got.Status)
	}
}

// A failed reopen whose pane survived its kill is parked with that pane
// still recorded. A retry must not resume the session beside it: it is
// refused while the pane cannot be killed, and goes ahead only once it is.
func TestReopenRetryWaitsForThePreviousPane(t *testing.T) {
	now := time.Date(2026, time.November, 14, 12, 0, 0, 0, time.UTC)
	d, rt, _, id, _ := reopened(t, &now)
	var mu sync.Mutex
	var events []string
	note := func(event string) func() {
		return func() { mu.Lock(); events = append(events, event); mu.Unlock() }
	}
	rt.mu.Lock()
	rt.killErr, rt.killHook, rt.launchHook = errors.New("tmux: server busy"), note("kill"), note("launch")
	rt.mu.Unlock()
	if _, err := d.Cancel(id); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.Get(id); !parked(got) || got.PaneID == "" {
		t.Fatalf("precondition: status=%s retained=%v pane=%q, want parked with its pane", got.Status, got.Retained, got.PaneID)
	}
	launches := len(rt.launchRequests())

	_, err := d.SendWithConfig(context.Background(), testConfig(), id, "again")

	if err == nil || !strings.Contains(err.Error(), "previous pane") {
		t.Fatalf("retry with the old pane standing = %v, want a refusal", err)
	}
	if len(rt.launchRequests()) != launches {
		t.Fatal("a reopen launched beside the earlier pane")
	}
	if got, _ := d.Get(id); !parked(got) || got.PaneID == "" || len(got.Turns) != 2 {
		t.Fatalf("a refused retry changed the run: status=%s pane=%q turns=%d", got.Status, got.PaneID, len(got.Turns))
	}

	rt.mu.Lock()
	rt.killErr = nil
	rt.mu.Unlock()
	mu.Lock()
	events = nil
	mu.Unlock()
	sent, err := d.SendWithConfig(context.Background(), testConfig(), id, "again")
	if err != nil || sent.TurnID != id+"#3" {
		t.Fatalf("retry once the pane can go = %+v, %v", sent, err)
	}
	waitForArmed(t, d, id)
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(events, []string{"kill", "launch"}) {
		t.Fatalf("pane events = %q, want the old pane killed before the reopen launched", events)
	}
}

// A restart that finds a parked run's pane still recorded (a failed
// reopen's kill never landed) kills it and clears it, keeping the run
// parked and its worktree.
func TestRestartKillsAParkedRunsLeftoverPane(t *testing.T) {
	configDir := claudeConfigDir(t)
	stateDir := t.TempDir()
	rec := seedReopenable(t, stateDir, configDir, "d-left", func(r *Record) { r.PaneID = "%7" })
	d := NewDispatcher(NewFileRecorder(stateDir))
	rt := newPaneRuntime(true)
	d.SetInteractiveRuntime(rt)

	markInterruptedAndReconcile(t, d)

	if !slices.Contains(rt.killedPanes(), "%7") {
		t.Fatalf("killed panes = %q, want the leftover %%7", rt.killedPanes())
	}
	got := mustLoad(t, stateDir, rec.ID)
	if !parked(got) || got.PaneID != "" || got.SessionID != rec.SessionID {
		t.Fatalf("after restart: status=%s retained=%v pane=%q session=%q", got.Status, got.Retained, got.PaneID, got.SessionID)
	}
}

// A leftover pane the restart cannot kill stays recorded, so a reopen still
// sees it and the kill is retried.
func TestRestartKeepsAParkedRunsUnkillablePane(t *testing.T) {
	configDir := claudeConfigDir(t)
	stateDir := t.TempDir()
	rec := seedReopenable(t, stateDir, configDir, "d-stuck", func(r *Record) { r.PaneID = "%7" })
	d := NewDispatcher(NewFileRecorder(stateDir))
	rt := newPaneRuntime(true)
	rt.killErr = errors.New("tmux: server busy")
	d.SetInteractiveRuntime(rt)

	markInterruptedAndReconcile(t, d)

	if got, _ := d.Get(rec.ID); !parked(got) || got.PaneID != "%7" {
		t.Fatalf("after a failed kill: status=%s pane=%q, want parked with %%7", got.Status, got.PaneID)
	}
	_, err := d.SendWithConfig(context.Background(), testConfig(), rec.ID, "more")
	if err == nil || !strings.Contains(err.Error(), "previous pane") {
		t.Fatalf("reopen beside an unkillable pane = %v, want a refusal", err)
	}
}
