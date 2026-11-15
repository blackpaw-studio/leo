package consult

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/session"
)

var reopenEnded = time.Date(2026, time.November, 14, 12, 0, 0, 0, time.UTC)

// claudeConfigDir points claude's config dir, as a reopen resolves it, at a
// fresh directory.
func claudeConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	return dir
}

// writeTranscript puts a session's transcript where claude keeps it for cwd
// under configDir, and returns its path.
func writeTranscript(t *testing.T, configDir, cwd, sessionID string, data []byte) string {
	t.Helper()
	path, err := session.JSONLPath(map[string]string{"CLAUDE_CONFIG_DIR": configDir}, cwd, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// seedReopenable persists a parked claude run the dispatcher no longer
// holds in memory, its session's transcript on disk under configDir.
func seedReopenable(t *testing.T, stateDir, configDir, id string, mut func(*Record)) Record {
	t.Helper()
	rec := Record{
		ID: id, Kind: "dispatch", Mode: ModeInteractive, Harness: "claude", Template: "claude", Model: "opus",
		Status: StatusClosed, Retained: true, ReleaseOnFinish: true, SessionID: "s-1", Cwd: t.TempDir(),
		Notify: true, CallerPaneID: "%9", CallerHarness: "claude", CallerSessionID: "$1",
		StartedAt: reopenEnded.Add(-time.Hour), EndedAt: reopenEnded,
		Turns: []Turn{{TurnID: id + "#1", Source: TurnSourceOrchestrator, Delivered: true, Outcome: TurnFinished, Text: "the answer", ResultCollectedAt: reopenEnded}},
	}
	rec.TranscriptPath = writeTranscript(t, configDir, rec.Cwd, rec.SessionID, nil)
	if mut != nil {
		mut(&rec)
	}
	recorder := NewFileRecorder(stateDir)
	if _, err := recorder.Open(rec); err != nil {
		t.Fatal(err)
	}
	if err := recorder.PersistRecord(rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func reopenDispatcher(t *testing.T, stateDir string) (*Dispatcher, *paneRuntime, *fakeNotificationDelivery) {
	t.Helper()
	d := NewDispatcher(NewFileRecorder(stateDir))
	now := reopenEnded.Add(time.Hour)
	d.now = sharedClock(&now)
	rt := newPaneRuntime(true)
	rt.arm, rt.empty = true, true
	d.SetInteractiveRuntime(rt)
	delivery := &fakeNotificationDelivery{ready: true}
	d.SetNotificationDelivery(delivery)
	return d, rt, delivery
}

// requireUntouched asserts a refused reopen changed nothing: the record is
// still parked with the same turns, no pane launched, no slot taken.
func requireUntouched(t *testing.T, d *Dispatcher, rt *paneRuntime, launchesBefore int, want Record) {
	t.Helper()
	got, err := d.Get(want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !parked(got) || len(got.Turns) != len(want.Turns) || got.ResumeError != want.ResumeError {
		t.Fatalf("a refused reopen changed the run: status=%s retained=%v turns=%d resume_error=%q", got.Status, got.Retained, len(got.Turns), got.ResumeError)
	}
	if n := len(rt.launchRequests()); n != launchesBefore {
		t.Fatalf("a refused reopen launched a pane: %d launches, want %d", n, launchesBefore)
	}
	if d.slots.InUse() != 0 || d.slots.Waiting() != 0 || d.exempt.InUse() != 0 {
		t.Fatalf("a refused reopen took a slot: pool used=%d waiting=%d exempt used=%d", d.slots.InUse(), d.slots.Waiting(), d.exempt.InUse())
	}
}

// finishReopenedTurn reports the reopened turn's submit and Stop, as the
// resumed claude session would.
func finishReopenedTurn(t *testing.T, d *Dispatcher, id, n, prompt, text string) {
	t.Helper()
	waitForArmed(t, d, id)
	if err := d.Report(id, claudeHook(t, "submit-"+n, "UserPromptSubmit", prompt)); err != nil {
		t.Fatal(err)
	}
	if err := d.Report(id, claudeStop(t, "stop-"+n, text, nil)); err != nil {
		t.Fatal(err)
	}
}

func TestSendToParkedRunReopensWithResume(t *testing.T) {
	configDir := claudeConfigDir(t)
	d, rt, delivery, id := startParkable(t, t.TempDir(), true, "the answer")
	d.SweepNotifications(context.Background())
	parkedRec := requireParked(t, d, id, id+"#1")
	writeTranscript(t, configDir, parkedRec.Worktree, parkedRec.SessionID, nil)
	const followUp = "what was my first instruction?"

	sent, err := d.SendWithConfig(context.Background(), testConfig(), id, followUp)
	if err != nil {
		t.Fatal(err)
	}

	if sent.TurnID != id+"#2" || sent.Queued {
		t.Fatalf("sent = %+v, want turn #2 launched at once", sent)
	}
	launches := rt.launchRequests()
	if len(launches) != 2 {
		t.Fatalf("launches = %d, want the opening and the reopen", len(launches))
	}
	got := launches[1]
	if got.ResumeSessionID != parkedRec.SessionID || !got.Reopen || got.Cwd != parkedRec.Worktree || got.Template != parkedRec.Template ||
		got.Harness != "claude" || !slices.Equal(got.Environments, parkedRec.InheritedEnvironments) || got.Prompt != followUp {
		t.Fatalf("reopen launch = %+v", got)
	}
	if rec := status(d, id); rec.Status.Terminal() || rec.PaneID == "" {
		t.Fatalf("reopened run status=%s pane=%q", rec.Status, rec.PaneID)
	}

	finishReopenedTurn(t, d, id, "2", followUp, "you said hello")
	d.SweepNotifications(context.Background())
	if len(delivery.calls) != 2 || !strings.Contains(delivery.calls[1], "you said hello") {
		t.Fatalf("delivery = %q", delivery.calls)
	}
	requireParked(t, d, id, id+"#2")
	d.Sweep(time.Time{})
	d.SweepNotifications(context.Background())
	if len(delivery.calls) != 2 {
		t.Fatalf("the reopened turn's result was delivered again: %q", delivery.calls)
	}
}

// queuedReopen parks a run, fills the pool's only slot, queues an earlier
// dispatch behind it, then sends the parked run a follow-up.
func queuedReopen(t *testing.T, stateDir string) (d *Dispatcher, rt *paneRuntime, id, earlier string, parkedRec Record) {
	t.Helper()
	configDir := claudeConfigDir(t)
	d, rt, _, id = startParkable(t, stateDir, false, "the answer")
	d.SweepNotifications(context.Background())
	parkedRec = requireParked(t, d, id, id+"#1")
	writeTranscript(t, configDir, parkedRec.Cwd, parkedRec.SessionID, nil)
	cfg := testConfigMax(1)
	d.ApplyConfig(cfg)
	if !d.slots.TryAcquire() {
		t.Fatal("could not occupy the pool's only slot")
	}
	started, err := d.Start(context.Background(), cfg, Request{Template: "claude", Prompt: "earlier work", Cwd: t.TempDir(), Mode: ModeInteractive, Kind: "dispatch"})
	if err != nil || !started.Queued {
		t.Fatalf("earlier dispatch = %+v, %v; want it queued", started, err)
	}
	sent, err := d.SendWithConfig(context.Background(), cfg, id, "more")
	if err != nil {
		t.Fatal(err)
	}
	if !sent.Queued || sent.TurnID != id+"#2" {
		t.Fatalf("sent = %+v, want turn #2 queued", sent)
	}
	return d, rt, id, started.ID, parkedRec
}

func TestReopenQueuesUnderCap(t *testing.T) {
	d, rt, id, earlier, parkedRec := queuedReopen(t, t.TempDir())
	if n := len(rt.launchRequests()); n != 1 {
		t.Fatalf("launches = %d while every slot is taken, want only the opening", n)
	}
	if rec := status(d, id); rec.Status != StatusQueued || !rec.Retained {
		t.Fatalf("queued reopen: status=%s retained=%v", rec.Status, rec.Retained)
	}

	d.slots.Release() // the holder leaves: the dispatch that queued first goes first
	waitFor(t, func() bool { return len(rt.launchRequests()) >= 2 })
	if l := rt.launchRequests()[1]; l.ID != earlier {
		t.Fatalf("the slot went to %s, want %s (queued first)", l.ID, earlier)
	}
	if _, err := d.Cancel(earlier); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(rt.launchRequests()) >= 3 })
	if l := rt.launchRequests()[2]; l.ID != id || l.ResumeSessionID != parkedRec.SessionID || l.Prompt != "more" {
		t.Fatalf("reopen launch = %+v", l)
	}
	waitForArmed(t, d, id)
}

func TestSecondSendWhileReopenQueuedRejected(t *testing.T) {
	d, rt, id, _, _ := queuedReopen(t, t.TempDir())

	_, err := d.SendWithConfig(context.Background(), testConfigMax(1), id, "and another")

	if err == nil || !strings.Contains(err.Error(), "queued for a concurrency slot") {
		t.Fatalf("second send error = %v", err)
	}
	if rec := status(d, id); len(rec.Turns) != 2 {
		t.Fatalf("turns = %d, want the parked turn and the queued reopen", len(rec.Turns))
	}
	if n := len(rt.launchRequests()); n != 1 {
		t.Fatalf("launches = %d, want only the opening", n)
	}
}

func testConfigMax(n int) *config.Config {
	cfg := testConfig()
	cfg.Defaults.Dispatch.MaxConcurrent = &n
	return cfg
}

func TestReopenRejectsNotResumable(t *testing.T) {
	configDir := claudeConfigDir(t)
	for _, c := range []struct {
		name string
		mut  func(*Record)
		want string
	}{
		{"no session id", func(r *Record) { r.SessionID = "" }, "no session id was recorded"},
		{"an earlier reopen failed", func(r *Record) { r.ResumeError = "could not resume session s-1 (harness started s-2)" }, "could not resume session s-1 (harness started s-2)"},
		{"harness cannot resume", func(r *Record) { r.Harness, r.Template = "opencode", "opencode" }, `harness "opencode" cannot resume an interactive session`},
	} {
		t.Run(c.name, func(t *testing.T) {
			stateDir := t.TempDir()
			rec := seedReopenable(t, stateDir, configDir, "d-nores", c.mut)
			d, rt, _ := reopenDispatcher(t, stateDir)

			_, err := d.SendWithConfig(context.Background(), testConfig(), rec.ID, "more")

			want := "dispatch d-nores closed and is not resumable: " + c.want + "; start a new dispatch"
			if err == nil || err.Error() != want {
				t.Fatalf("error = %v, want %q", err, want)
			}
			requireUntouched(t, d, rt, 0, rec)
		})
	}
}

func TestReopenRejectsMissingTranscript(t *testing.T) {
	configDir := claudeConfigDir(t)
	for _, c := range []struct {
		name string
		mut  func(*Record)
	}{
		{"recorded path gone", func(r *Record) { _ = os.Remove(r.TranscriptPath) }},
		{"no recorded path and none where claude keeps it", func(r *Record) { _ = os.Remove(r.TranscriptPath); r.TranscriptPath = "" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			stateDir := t.TempDir()
			rec := seedReopenable(t, stateDir, configDir, "d-notr", c.mut)
			d, rt, _ := reopenDispatcher(t, stateDir)

			_, err := d.SendWithConfig(context.Background(), testConfig(), rec.ID, "more")

			if err == nil || !strings.Contains(err.Error(), "dispatch d-notr closed and is not resumable: session s-1's transcript is missing") {
				t.Fatalf("error = %v", err)
			}
			requireUntouched(t, d, rt, 0, rec)
		})
	}
}

func TestReopenRejectsConfigDirMismatch(t *testing.T) {
	configDir := claudeConfigDir(t)
	stateDir := t.TempDir()
	other := t.TempDir()
	rec := seedReopenable(t, stateDir, configDir, "d-acct", func(r *Record) {
		r.TranscriptPath = writeTranscript(t, other, r.Cwd, r.SessionID, nil)
	})
	d, rt, _ := reopenDispatcher(t, stateDir)

	_, err := d.SendWithConfig(context.Background(), testConfig(), rec.ID, "more")

	want := fmt.Sprintf("is not under the claude config dir this dispatch would reopen with (%s)", configDir)
	if err == nil || !strings.Contains(err.Error(), "closed and is not resumable") || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
	requireUntouched(t, d, rt, 0, rec)
}

func TestReopenAfterEviction(t *testing.T) {
	configDir := claudeConfigDir(t)
	stateDir := t.TempDir()
	rec := seedReopenable(t, stateDir, configDir, "d-evict", nil)
	d, rt, delivery := reopenDispatcher(t, stateDir)

	sent, err := d.SendWithConfig(context.Background(), testConfig(), rec.ID, "more")
	if err != nil {
		t.Fatal(err)
	}

	if sent.TurnID != rec.ID+"#2" || sent.Queued {
		t.Fatalf("sent = %+v", sent)
	}
	launches := rt.launchRequests()
	if len(launches) != 1 || launches[0].ResumeSessionID != "s-1" || launches[0].Cwd != rec.Cwd || launches[0].Prompt != "more" {
		t.Fatalf("launches = %+v", launches)
	}
	disk := mustLoad(t, stateDir, rec.ID)
	if disk.Status.Terminal() || len(disk.Turns) != 2 || !disk.EndedAt.IsZero() {
		t.Fatalf("persisted reopen: status=%s turns=%d ended=%v", disk.Status, len(disk.Turns), disk.EndedAt)
	}
	finishReopenedTurn(t, d, rec.ID, "2", "more", "second answer")
	d.SweepNotifications(context.Background())
	if len(delivery.calls) != 1 || !strings.Contains(delivery.calls[0], "second answer") {
		t.Fatalf("delivery = %q", delivery.calls)
	}
	requireParked(t, d, rec.ID, rec.ID+"#2")
	if disk := mustLoad(t, stateDir, rec.ID); !parked(disk) || len(disk.Turns) != 2 {
		t.Fatalf("persisted after parking again: status=%s retained=%v turns=%d", disk.Status, disk.Retained, len(disk.Turns))
	}
}

func TestReopenWorktreeDeletedExternally(t *testing.T) {
	configDir := claudeConfigDir(t)
	d, rt, id := parkIsolated(t, t.TempDir())
	parkedRec, _ := d.Get(id)
	writeTranscript(t, configDir, parkedRec.Worktree, parkedRec.SessionID, nil)
	if err := os.RemoveAll(parkedRec.Worktree); err != nil {
		t.Fatal(err)
	}

	_, err := d.SendWithConfig(context.Background(), testConfig(), id, "more")

	if err == nil || !strings.Contains(err.Error(), "closed and is not resumable: retained worktree is unavailable") {
		t.Fatalf("error = %v", err)
	}
	requireUntouched(t, d, rt, 1, parkedRec)
}

func TestReopenNestedRespectsDescendantCap(t *testing.T) {
	configDir := claudeConfigDir(t)
	stateDir := t.TempDir()
	d, rt, _ := reopenDispatcher(t, stateDir)
	d.runs["d-root"] = newRunState(Record{ID: "d-root", Kind: "dispatch", Mode: ModeHeadless, Status: StatusRunning, StartedAt: reopenEnded}, &durableTestHandle{}, make(chan struct{}), nil)
	for i := range MaxLiveDescendantsPerRoot {
		child := fmt.Sprintf("d-live%d", i)
		d.runs[child] = newRunState(Record{ID: child, Kind: "dispatch", Mode: ModeInteractive, Status: StatusRunning, ParentDispatchID: "d-root", SlotExempt: true, StartedAt: reopenEnded}, &durableTestHandle{}, make(chan struct{}), nil)
	}
	rec := seedReopenable(t, stateDir, configDir, "d-kid", func(r *Record) { r.ParentDispatchID, r.SlotExempt = "d-root", true })

	_, err := d.SendWithConfig(context.Background(), testConfig(), rec.ID, "more")

	if err == nil || !strings.Contains(err.Error(), "descendant limit") {
		t.Fatalf("error = %v", err)
	}
	requireUntouched(t, d, rt, 0, rec)
}

// The resumed session's transcript still holds the earlier turns' messages:
// only what the reopened turn adds is counted again.
func TestReopenUsageNotDoubleCounted(t *testing.T) {
	configDir := claudeConfigDir(t)
	stateDir := t.TempDir()
	streamed := fixture(t, "streamed.jsonl")
	rec := seedReopenable(t, stateDir, configDir, "d-usage", func(r *Record) {
		r.SessionID = "sess-1"
		r.TranscriptPath = writeTranscript(t, configDir, r.Cwd, r.SessionID, streamed)
		r.UsageInvocations = []InvocationUsage{{InputTokens: harness.Int64(2000), OutputTokens: harness.Int64(60), Completed: true}}
		r.InputTokens, r.OutputTokens = harness.Int64(2000), harness.Int64(60)
	})
	d, _, _ := reopenDispatcher(t, stateDir)
	files := &lockedTranscripts{files: memTranscripts{}}
	files.set(rec.TranscriptPath, streamed)
	d.liveUsage = liveUsageConfig{poll: time.Hour, read: files.read, path: func(string, string) (string, error) { return rec.TranscriptPath, nil }}

	if _, err := d.SendWithConfig(context.Background(), testConfig(), rec.ID, "more"); err != nil {
		t.Fatal(err)
	}
	files.set(rec.TranscriptPath, append(append([]byte(nil), streamed...), fixture(t, "next_turn.jsonl")...))
	waitForArmed(t, d, rec.ID)
	mustReport(t, d, rec.ID, liveHook(t, "s2", "UserPromptSubmit", "more", rec.TranscriptPath))
	refreshLive(t, d, rec.ID)

	wantTokens(t, recordOf(t, d, rec.ID), 2000+1323, 60+9, true)
}

// reopenedWithCost reopens a parked run whose earlier panes counted cost.
func reopenedWithCost(t *testing.T, cost float64) (*Dispatcher, string) {
	t.Helper()
	configDir := claudeConfigDir(t)
	stateDir := t.TempDir()
	rec := seedReopenable(t, stateDir, configDir, "d-cost", func(r *Record) {
		r.UsageInvocations = []InvocationUsage{{CostUSD: harness.Float64(cost), Completed: true}}
		r.CostUSD = harness.Float64(cost)
	})
	d, _, _ := reopenDispatcher(t, stateDir)
	if _, err := d.SendWithConfig(context.Background(), testConfig(), rec.ID, "more"); err != nil {
		t.Fatal(err)
	}
	waitForArmed(t, d, rec.ID)
	return d, rec.ID
}

func wantCost(t *testing.T, d *Dispatcher, id string, want float64) {
	t.Helper()
	got := recordOf(t, d, id).CostUSD
	if got == nil {
		t.Fatalf("cost = nil, want %v", want)
	}
	if math.Abs(*got-want) > 1e-9 {
		t.Fatalf("cost = %v, want %v", *got, want)
	}
}

// Claude restores a resumed session's running cost, so a reopened pane's
// reports already include what the earlier panes counted.
func TestReopenCostNotDoubleCounted(t *testing.T) {
	d, id := reopenedWithCost(t, 1.0)

	d.ApplyBridgeUsage(id, json.RawMessage(`{"cost":{"usd":1.25}}`))
	wantCost(t, d, id, 1.25)
	d.ApplyBridgeUsage(id, json.RawMessage(`{"cost":{"usd":1.5}}`))
	wantCost(t, d, id, 1.5)
}

// A resumed session reporting less than the earlier panes counted did not
// carry its cost over: its reports are its own.
func TestReopenCostNotCarriedCountsWhole(t *testing.T) {
	d, id := reopenedWithCost(t, 1.0)

	d.ApplyBridgeUsage(id, json.RawMessage(`{"cost":{"usd":0.25}}`))
	wantCost(t, d, id, 1.25)
	d.ApplyBridgeUsage(id, json.RawMessage(`{"cost":{"usd":0.5}}`))
	wantCost(t, d, id, 1.5)
}
