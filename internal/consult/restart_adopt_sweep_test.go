package consult

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

func TestLastAssistantTextSince(t *testing.T) {
	path := filepath.Join("testdata", "transcript", "adopted.jsonl")
	for _, tc := range []struct {
		since time.Time
		want  string
	}{
		{time.Date(2026, time.October, 19, 8, 59, 0, 0, time.UTC), "ADOPT-OK"},
		{time.Date(2026, time.October, 19, 8, 57, 0, 0, time.UTC), "ADOPT-OK"},
		{time.Date(2026, time.October, 19, 9, 2, 0, 0, time.UTC), ""},
	} {
		got, err := lastAssistantTextSince(readTranscriptFrom, path, tc.since)
		if err != nil || got != tc.want {
			t.Fatalf("since %s: %q, %v; want %q", tc.since, got, err, tc.want)
		}
	}
	if got, err := lastAssistantTextSince(readTranscriptFrom, filepath.Join(t.TempDir(), "missing.jsonl"), time.Time{}); err != nil || got != "" {
		t.Fatalf("missing transcript: %q, %v", got, err)
	}
}

func TestPaneSnapshotCapturesAndClassifiesThePane(t *testing.T) {
	for _, tc := range []struct {
		screen string
		empty  bool
	}{
		{"╭───╮\n│ >  │\n╰───╯\n  ? for shortcuts\n", true},
		{"✻ Running… (12s · esc to interrupt)\n", false},
	} {
		var argv []string
		r := NewInteractiveRuntime("", nil, nil, "tmux", "leo")
		r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
			argv = args
			return exec.Command("printf", "%s", tc.screen)
		}
		got, empty, err := r.PaneSnapshot("%9")
		if err != nil || got != tc.screen || empty != (tmux.ClaudeComposerClassifier(tc.screen) == tmux.ComposerEmpty) {
			t.Fatalf("PaneSnapshot = %q, %v, %v", got, empty, err)
		}
		if want := tmux.Args("capture-pane", "-p", "-t", "%9"); !reflect.DeepEqual(argv, want) {
			t.Fatalf("argv = %q, want %q", argv, want)
		}
	}
}

// adoptRig is a dispatcher that adopted workingRecord's run at adoptNow,
// with a clock the test moves and a pane the test paints.
type adoptRig struct {
	d   *Dispatcher
	rt  *adoptingRuntime
	now time.Time
}

func newAdoptRig(t *testing.T, transcript string) *adoptRig {
	t.Helper()
	rec := workingRecord()
	rec.SessionID = "s-adopt"
	return newAdoptRigFor(t, rec, transcript)
}

// newAdoptRigFor adopts rec, guessing transcript as its session's path.
func newAdoptRigFor(t *testing.T, rec Record, transcript string) *adoptRig {
	t.Helper()
	g := &adoptRig{now: adoptNow}
	g.rt = &adoptingRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{alive: true}, probe: ownedProbe(""), snapshot: "✻ Running… (1s)"}
	stateDir := t.TempDir()
	restartRecord(t, stateDir, rec)
	g.d = NewDispatcher(NewFileRecorder(stateDir))
	g.d.now = func() time.Time { return g.now }
	g.d.liveUsage.path = func(cwd, sessionID string) (string, error) {
		if sessionID != "s-adopt" {
			t.Errorf("transcript of session %q", sessionID)
		}
		return transcript, nil
	}
	g.d.adoptTimeout = time.Minute
	g.d.SetInteractiveRuntime(g.rt)
	g.d.MarkInterrupted()
	return g
}

func (g *adoptRig) sweepAt(at time.Time) {
	g.now = at
	g.d.Sweep(at)
}

func TestAdoptedRunSettlesAtTheDeadline(t *testing.T) {
	g := newAdoptRig(t, "")
	deadline := adoptNow.Add(time.Minute)
	g.sweepAt(adoptNow)
	g.sweepAt(deadline.Add(-time.Nanosecond))
	if got := mustGet(t, g.d, "d-adopt"); got.Status != StatusRunning || g.rt.killCount() != 0 {
		t.Fatalf("status=%s kills=%d before the deadline", got.Status, g.rt.killCount())
	}
	g.sweepAt(deadline)
	got := mustGet(t, g.d, "d-adopt")
	if got.Status != StatusFailed || got.Error != "daemon restarted" || got.Turns[0].Outcome != TurnLost {
		t.Fatalf("status=%s error=%q turn=%+v; want settled as daemon restarted", got.Status, got.Error, got.Turns[0])
	}
	if n, ok := got.Notifications[closeNoticeKey("d-adopt")]; !ok || !strings.Contains(n.Message, "daemon restarted") {
		t.Fatalf("close notice = %+v (%v)", n, ok)
	}
	if g.rt.killCount() != 1 {
		t.Fatalf("kills=%d, want the stuck pane killed", g.rt.killCount())
	}
	if g.d.slots.InUse() != 0 {
		t.Fatalf("slots in use = %d after settling", g.d.slots.InUse())
	}
}

func TestAdoptedRunWhosePaneChangesIsLive(t *testing.T) {
	g := newAdoptRig(t, "")
	g.sweepAt(adoptNow)
	g.rt.setSnapshot("✻ Running… (6s)", false)
	g.sweepAt(adoptNow.Add(5 * time.Second))
	g.sweepAt(adoptNow.Add(2 * time.Minute))
	if got := mustGet(t, g.d, "d-adopt"); got.Status != StatusRunning || g.rt.killCount() != 0 {
		t.Fatalf("status=%s kills=%d; a working pane must stay adopted", got.Status, g.rt.killCount())
	}
}

func TestAdoptedRunThatReportsIsLive(t *testing.T) {
	g := newAdoptRig(t, "")
	if err := g.d.Report("d-adopt", hookReport("PreToolUse", map[string]string{"tool_use_id": "tu9"})); err != nil {
		t.Fatal(err)
	}
	g.sweepAt(adoptNow.Add(2 * time.Minute))
	if got := mustGet(t, g.d, "d-adopt"); got.Status != StatusRunning {
		t.Fatalf("status=%s; a reporting run must stay adopted", got.Status)
	}
}

func TestAdoptedRunWhoseBridgeSaysHelloIsLive(t *testing.T) {
	g := newAdoptRig(t, "")
	g.d.NoteBridgeHello("d-adopt")
	g.sweepAt(adoptNow.Add(2 * time.Minute))
	if got := mustGet(t, g.d, "d-adopt"); got.Status != StatusRunning {
		t.Fatalf("status=%s; a run whose mod said hello must stay adopted", got.Status)
	}
}

// A turn that finished while the daemon was down leaves an idle composer
// and no report: once the grace for retried reports passes, its result is
// read from the transcript and reaches the caller.
func TestAdoptedIdleRunFinishesFromItsTranscript(t *testing.T) {
	g := newAdoptRig(t, filepath.Join("testdata", "transcript", "adopted.jsonl"))
	g.rt.setSnapshot("│ > │", true)
	g.sweepAt(adoptNow)
	g.sweepAt(adoptNow.Add(adoptIdleGrace - time.Nanosecond))
	if got := mustGet(t, g.d, "d-adopt"); got.Turns[0].Outcome != "" {
		t.Fatalf("turn closed before the grace: %+v", got.Turns[0])
	}
	g.sweepAt(adoptNow.Add(adoptIdleGrace))
	got := mustGet(t, g.d, "d-adopt")
	if got.Status != StatusIdle || got.Turns[0].Outcome != TurnFinished || got.Turns[0].Text != "ADOPT-OK" {
		t.Fatalf("status=%s turn=%+v; want finished with the transcript's text", got.Status, got.Turns[0])
	}
	if n, ok := got.Notifications["d-adopt#1"]; !ok || n.Disposition != NotificationPending {
		t.Fatalf("turn notification = %+v (%v)", n, ok)
	}
	if g.rt.killCount() != 0 {
		t.Fatal("reconciling killed the pane")
	}
}

// The transcript a hook reported before the restart is where the session
// is, even when claude's config dir is not the default one the session id
// alone would point at.
func TestAdoptedIdleRunReadsTheTranscriptItsHooksReported(t *testing.T) {
	rec := workingRecord()
	rec.SessionID = "s-adopt"
	rec.TranscriptPath = filepath.Join("testdata", "transcript", "adopted.jsonl")
	g := newAdoptRigFor(t, rec, filepath.Join(t.TempDir(), "default-config-dir.jsonl"))
	g.rt.setSnapshot("│ > │", true)
	g.sweepAt(adoptNow)
	g.sweepAt(adoptNow.Add(adoptIdleGrace))
	if got := mustGet(t, g.d, "d-adopt"); got.Turns[0].Outcome != TurnFinished || got.Turns[0].Text != "ADOPT-OK" {
		t.Fatalf("turn=%+v; want finished with the reported transcript's text", got.Turns[0])
	}
}

// A hook's transcript path is kept on the record, so a restarted daemon
// still knows it.
func TestHookReportedTranscriptPathIsPersisted(t *testing.T) {
	g := newAdoptRig(t, "")
	const path = "/cfg/projects/-ws/s-adopt.jsonl"
	if err := g.d.Report("d-adopt", hookReport("PreToolUse", map[string]string{"tool_use_id": "tu9", "transcript_path": path})); err != nil {
		t.Fatal(err)
	}
	for _, rec := range g.d.Records() {
		if rec.ID == "d-adopt" && rec.TranscriptPath != path {
			t.Fatalf("persisted transcript path = %q, want %q", rec.TranscriptPath, path)
		}
	}
}

func TestAdoptedIdleRunWithoutATranscriptLosesItsTurn(t *testing.T) {
	g := newAdoptRig(t, filepath.Join(t.TempDir(), "missing.jsonl"))
	g.rt.setSnapshot("│ > │", true)
	g.sweepAt(adoptNow)
	g.sweepAt(adoptNow.Add(adoptIdleGrace))
	got := mustGet(t, g.d, "d-adopt")
	if got.Turns[0].Outcome != TurnLost || got.Turns[0].Text != adoptNoResultText {
		t.Fatalf("turn=%+v; want lost with the no-result notice", got.Turns[0])
	}
	if n, ok := got.Notifications["d-adopt#1"]; !ok || !strings.Contains(n.Message, "d-adopt#1") {
		t.Fatalf("turn notification = %+v (%v)", n, ok)
	}
}

func TestAdoptedRunWhosePaneGoesAwayClosesAsPaneGone(t *testing.T) {
	g := newAdoptRig(t, "")
	g.rt.setGone()
	g.sweepAt(adoptNow)
	g.sweepAt(adoptNow.Add(finalReportGrace))
	got := mustGet(t, g.d, "d-adopt")
	if n, ok := got.Notifications[closeNoticeKey("d-adopt")]; !got.Status.Terminal() || !ok || !strings.Contains(n.Message, closeReasonPaneGone) {
		t.Fatalf("status=%s close notice=%+v (%v); want closed as pane gone", got.Status, n, ok)
	}
}

func hookReport(event string, fields map[string]string) HookReport {
	payload := map[string]string{"hook_event_name": event}
	for k, v := range fields {
		payload[k] = v
	}
	b, _ := json.Marshal(payload)
	return HookReport{EventID: event + fields["tool_use_id"], Payload: b}
}

// A run that was idle when the daemon went down shows nothing but an empty
// composer, which is all an idle run should show: it stays adopted.
func TestAdoptedIdleRunWithNothingInFlightStays(t *testing.T) {
	g := &adoptRig{now: adoptNow}
	g.rt = &adoptingRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{alive: true}, probe: ownedProbe(""), snapshot: "│ > │", snapEmpty: true}
	stateDir := t.TempDir()
	rec := workingRecord()
	rec.Status, rec.Turns[0].Outcome, rec.Turns[0].SlotHeld, rec.Turns[0].Text = StatusIdle, TurnFinished, false, "earlier answer"
	restartRecord(t, stateDir, rec)
	g.d = NewDispatcher(NewFileRecorder(stateDir))
	g.d.now = func() time.Time { return g.now }
	g.d.adoptTimeout = time.Minute
	g.d.SetInteractiveRuntime(g.rt)
	g.d.MarkInterrupted()
	g.sweepAt(adoptNow)
	g.sweepAt(adoptNow.Add(adoptIdleGrace))
	g.sweepAt(adoptNow.Add(2 * time.Minute))
	got := mustGet(t, g.d, "d-adopt")
	if got.Status != StatusIdle || got.Turns[0].Text != "earlier answer" || g.rt.killCount() != 0 {
		t.Fatalf("status=%s turn=%+v kills=%d; an idle adopted run must stay as it was", got.Status, got.Turns[0], g.rt.killCount())
	}
}

// A bridged run's turn hooks are superseded by its mod's reports, but they
// still say where the transcript is, and a turn without tools sends no
// other hook that would.
func TestSupersededHookStillRecordsTheTranscriptPath(t *testing.T) {
	g := newAdoptRig(t, "")
	const path = "/cfg/projects/-ws/s-adopt.jsonl"
	if err := g.d.ReportSupersededStop("d-adopt", hookReport("UserPromptSubmit", map[string]string{"transcript_path": path})); err != nil {
		t.Fatal(err)
	}
	for _, rec := range g.d.Records() {
		if rec.ID == "d-adopt" && rec.TranscriptPath != path {
			t.Fatalf("persisted transcript path = %q, want %q", rec.TranscriptPath, path)
		}
	}
}
