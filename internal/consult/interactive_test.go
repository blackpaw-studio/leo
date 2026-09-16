package consult

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"sync"
	"testing"
	"time"
)

type fakeInteractiveRuntime struct {
	mu           sync.Mutex
	pane         string
	alive        bool
	injected     []string
	arm          bool
	empty        bool
	kill         int
	injectErr    error
	placements   []string
	sessionAlive *bool
	killHook     func()
	launchHook   func()
	killErr      error
	layouts      []string
}

type recoveringInteractiveRuntime struct {
	*fakeInteractiveRuntime
	pane string
}

type presenceInteractiveRuntime struct {
	*fakeInteractiveRuntime
	presence PanePresence
	probeErr error
}

func (r presenceInteractiveRuntime) PanePresence(string) (PanePresence, error) {
	return r.presence, r.probeErr
}

func (r recoveringInteractiveRuntime) FindPaneByDispatchID(windowID, dispatchID string) (string, error) {
	if windowID != "@7" || dispatchID != "d-cafe" {
		return "", errors.New("unexpected recovery lookup")
	}
	return r.pane, nil
}

func (r *fakeInteractiveRuntime) Launch(_ context.Context, req LaunchRequest) (string, string, error) {
	if r.launchHook != nil {
		r.launchHook()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pane == "" {
		r.pane = "%1"
	}
	r.alive = true
	r.placements = append(r.placements, req.Placement.Kind)
	return r.pane, "w", nil
}
func (r *fakeInteractiveRuntime) SessionAlive(string) (bool, error) {
	if r.sessionAlive == nil {
		return true, nil
	}
	return *r.sessionAlive, nil
}
func (r *fakeInteractiveRuntime) ReapplyLayout(target string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.layouts = append(r.layouts, target)
	return nil
}
func (r *fakeInteractiveRuntime) Inject(_ context.Context, _ string, text string, arm func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.injected = append(r.injected, text)
	if r.injectErr != nil {
		return r.injectErr
	}
	if r.arm {
		return arm()
	}
	return nil
}
func (r *fakeInteractiveRuntime) Alive(string) bool { r.mu.Lock(); defer r.mu.Unlock(); return r.alive }
func (r *fakeInteractiveRuntime) Kill(string) error {
	if r.killHook != nil {
		r.killHook()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kill++
	if r.killErr != nil {
		return r.killErr
	}
	r.alive = false
	return nil
}
func (r *fakeInteractiveRuntime) ComposerEmpty(string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.empty
}
func (r *fakeInteractiveRuntime) setAlive(alive bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alive = alive
}
func (r *fakeInteractiveRuntime) injectionCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.injected)
}

func (r *fakeInteractiveRuntime) firstInjection() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.injected) == 0 {
		return ""
	}
	return r.injected[0]
}
func (r *fakeInteractiveRuntime) killCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.kill
}

func waitForInjection(t *testing.T, r *fakeInteractiveRuntime) {
	t.Helper()
	deadline := time.After(time.Second)
	for r.injectionCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("opening injection did not run")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestConcurrentViewerPlacementCap(t *testing.T) {
	max := 3
	cfg := testConfig()
	cfg.Defaults.Dispatch.Viewer.MaxPanes = &max
	var d *Dispatcher
	var headlessMu sync.Mutex
	var headless []string
	d = NewDispatcherWithOnStart(newFakeRecorder(), context.Background(), func(rec Record) string {
		p := d.placement.Decide(rec, ViewerOverrides{}, cfg, d.Records)
		headlessMu.Lock()
		headless = append(headless, p.Kind)
		headlessMu.Unlock()
		if p.Kind == "split" {
			return "%h"
		}
		return "@h"
	})
	placementCtx, cancelPlacement := context.WithCancel(context.Background())
	defer cancelPlacement()
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/sleep", "60")
	}
	rt := &fakeInteractiveRuntime{}
	d.SetInteractiveRuntime(rt)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			mode := ModeInteractive
			if i%2 == 0 {
				mode = ModeHeadless
			}
			_, _ = d.Start(placementCtx, cfg, Request{Template: "claude", Prompt: "x", Cwd: t.TempDir(), Mode: mode, Kind: "dispatch", CallerPaneID: "%1", CallerSessionID: "$1", CallerWindowID: "@1"})
		}()
	}
	close(start)
	wg.Wait()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	n := 0
	for _, kind := range rt.placements {
		if kind == "split" {
			n++
		}
	}
	for _, kind := range headless {
		if kind == "split" {
			n++
		}
	}
	if n > max {
		t.Fatalf("split placements=%d max=%d: interactive=%v headless=%v", n, max, rt.placements, headless)
	}
}

func TestViewerPlacementReservationsIgnoreUnpublishedRecords(t *testing.T) {
	max := 2
	cfg := testConfig()
	cfg.Defaults.Dispatch.Viewer.MaxPanes = &max
	c := NewViewerPlacementCoordinator()
	records := []Record{}
	base := Record{Kind: "dispatch", CallerPaneID: "%1", CallerSessionID: "$1", CallerWindowID: "@1"}
	first := base
	first.ID = "first"
	if got := c.Decide(first, ViewerOverrides{}, cfg, func() []Record { return records }); got.Kind != "split" {
		t.Fatalf("first=%s", got.Kind)
	}
	records = append(records, first) // queued/published record has no pane yet.
	second := base
	second.ID = "second"
	if got := c.Decide(second, ViewerOverrides{}, cfg, func() []Record { return records }); got.Kind != "split" {
		t.Fatalf("second=%s", got.Kind)
	}
	third := base
	third.ID = "third"
	if got := c.Decide(third, ViewerOverrides{}, cfg, func() []Record { return records }); got.Kind != "window" {
		t.Fatalf("third=%s", got.Kind)
	}
}

func releaseState(t *testing.T, status Status) (*Dispatcher, *fakeInteractiveRuntime, string) {
	t.Helper()
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{alive: true}
	d.SetInteractiveRuntime(rt)
	id := "d-release"
	d.runs[id] = &runState{record: Record{ID: id, Kind: "dispatch", Mode: ModeInteractive, Status: status, PaneID: "%9", ViewerKind: "split", CallerPaneID: "%1", CallerSessionID: "$1", CallerWindowID: "@1"}, handle: nopHandle{}, done: make(chan struct{})}
	return d, rt, id
}

func TestReleaseAllowedStatuses(t *testing.T) {
	for _, status := range []Status{StatusIdle, StatusDone, StatusFailed, StatusCanceled, StatusClosed} {
		t.Run(string(status), func(t *testing.T) {
			d, _, id := releaseState(t, status)
			rec, err := d.Release(id)
			if err != nil || rec.Status != StatusReleased {
				t.Fatalf("Release=%+v,%v", rec, err)
			}
		})
	}
}

func TestSweepSkipsRunBeingReleased(t *testing.T) {
	d, rt, id := releaseState(t, StatusIdle)
	now := time.Now()
	d.now = func() time.Time { return now }
	d.runs[id].record.StartedAt = now.Add(-time.Hour)
	d.runs[id].record.Timeout = time.Second
	rt.killHook = func() { d.Sweep(now) }
	rec, err := d.Release(id)
	if err != nil || rec.Status != StatusReleased {
		t.Fatalf("Release=%+v,%v", rec, err)
	}
}

func TestCloseRecordedPaneClearsIDsAndUsesCallerWindow(t *testing.T) {
	rec := Record{PaneID: "%9", ViewerPaneID: "%9", ViewerKind: "split", CallerWindowID: "@7"}
	var killed, layout string
	d := NewDispatcher(nil)
	got, err := d.closeRecordedPane(rec, "%9", func(p string) error { killed = p; return nil }, func(w string) error { layout = w; return nil })
	if err != nil || got.PaneID != "" || got.ViewerPaneID != "" || killed != "%9" || layout != "@7" {
		t.Fatalf("cleanup=%+v err=%v killed=%q layout=%q", got, err, killed, layout)
	}
}

func TestCloseRecordedPaneFailureKeepsPaneAndRetry(t *testing.T) {
	d, _, id := releaseState(t, StatusCanceled)
	rec := d.runs[id].record
	got, err := d.closeRecordedPane(rec, rec.PaneID, func(string) error { return errors.New("tmux unavailable") }, nil)
	if err == nil || got.PaneID != "%9" || got.ViewerKind != "split" || !d.runs[id].killPending {
		t.Fatalf("cleanup=%+v err=%v retry=%v", got, err, d.runs[id].killPending)
	}
}

func TestSweepKillFailureAfterReleaseDoesNotResurrectPane(t *testing.T) {
	d, _, id := releaseState(t, StatusCanceled)
	rec := cloneRecord(d.runs[id].record)
	got, err := d.closeRecordedPane(rec, rec.PaneID, func(string) error {
		d.mu.Lock()
		d.runs[id].record.Status = StatusReleased
		d.runs[id].record.PaneID = ""
		d.runs[id].record.ViewerPaneID = ""
		d.runs[id].record.ViewerKind = ""
		d.mu.Unlock()
		return errors.New("stale sweep kill failed")
	}, nil)
	if err == nil || got.Status != StatusReleased || got.PaneID != "" || d.runs[id].killPending {
		t.Fatalf("cleanup=%+v err=%v retry=%v", got, err, d.runs[id].killPending)
	}
}

func TestSweepPanePresenceOutcomes(t *testing.T) {
	tests := []struct {
		name      string
		presence  PanePresence
		probeErr  error
		wantKills int
		wantPane  string
	}{
		{"present-alive", PanePresentAlive, nil, 1, ""},
		{"present-dead", PanePresentDead, nil, 1, ""},
		{"absent", PaneAbsent, nil, 0, ""},
		{"probe-error", PaneAbsent, errors.New("socket unavailable"), 0, "%9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, fake, id := releaseState(t, StatusCanceled)
			d.runs[id].killPending = true
			d.SetInteractiveRuntime(presenceInteractiveRuntime{fakeInteractiveRuntime: fake, presence: tt.presence, probeErr: tt.probeErr})
			d.Sweep(time.Now())
			if fake.killCount() != tt.wantKills || d.runs[id].record.PaneID != tt.wantPane {
				t.Fatalf("kills=%d pane=%q", fake.killCount(), d.runs[id].record.PaneID)
			}
		})
	}
}

func TestSweepActiveProbeErrorLeavesRunUntouched(t *testing.T) {
	d, fake, id := releaseState(t, StatusRunning)
	d.runs[id].killPending = false
	d.SetInteractiveRuntime(presenceInteractiveRuntime{fakeInteractiveRuntime: fake, probeErr: errors.New("socket unavailable")})
	d.Sweep(time.Now())
	if got := d.runs[id].record.Status; got != StatusRunning {
		t.Fatalf("status=%s", got)
	}
}

func TestPostLaunchCancellationKillFailureKeepsReservation(t *testing.T) {
	cfg := testConfig()
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{killErr: errors.New("tmux unavailable")}
	d.SetInteractiveRuntime(rt)
	rt.launchHook = func() {
		d.mu.Lock()
		for _, state := range d.runs {
			d.finishInteractiveLocked(state, StatusCanceled)
		}
		d.mu.Unlock()
	}
	started, err := d.Start(context.Background(), cfg, Request{Template: "claude", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive, Kind: "dispatch", CallerPaneID: "%1", CallerSessionID: "$1", CallerWindowID: "@1"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start=%+v,%v", started, err)
	}
	d.mu.Lock()
	id := started.ID
	if id == "" {
		for candidate := range d.runs {
			id = candidate
		}
	}
	rec := cloneRecord(d.runs[id].record)
	d.mu.Unlock()
	d.placement.mu.Lock()
	_, reserved := d.placement.reserved[id]
	d.placement.mu.Unlock()
	if rec.PaneID == "" || !reserved || !d.runs[id].killPending {
		t.Fatalf("record=%+v reserved=%v retry=%v", rec, reserved, d.runs[id].killPending)
	}
}

func TestMarkInterruptedClearsKilledPaneAndReappliesLayout(t *testing.T) {
	stateDir := t.TempDir()
	recorder := NewFileRecorder(stateDir)
	h, err := recorder.Open(Record{ID: "d-restart-pane", Kind: "dispatch", Mode: ModeInteractive, Status: StatusRunning, PaneID: "%9", ViewerKind: "split", CallerWindowID: "@7"})
	if err != nil {
		t.Fatal(err)
	}
	_ = h.Close(StatusRunning, nil)
	rt := &fakeInteractiveRuntime{alive: true}
	d := NewDispatcher(recorder)
	d.SetInteractiveRuntime(rt)
	d.MarkInterrupted()
	got, err := LoadOne(stateDir, "d-restart-pane")
	if err != nil {
		t.Fatal(err)
	}
	if got.PaneID != "" || rt.killCount() != 1 || len(rt.layouts) != 1 || rt.layouts[0] != "@7" {
		t.Fatalf("record=%+v kills=%d layouts=%v", got, rt.killCount(), rt.layouts)
	}
}

func TestMarkInterruptedAdoptsSplitPaneByDispatchID(t *testing.T) {
	stateDir := t.TempDir()
	recorder := NewFileRecorder(stateDir)
	h, err := recorder.Open(Record{ID: "d-cafe", Kind: "dispatch", Template: "worker", Mode: ModeInteractive, Status: StatusRunning, ViewerKind: "split", ViewerTitle: "worker·cafe", CallerWindowID: "@7"})
	if err != nil {
		t.Fatal(err)
	}
	_ = h.Close(StatusRunning, nil)
	fake := &fakeInteractiveRuntime{alive: true}
	d := NewDispatcher(recorder)
	d.SetInteractiveRuntime(recoveringInteractiveRuntime{fakeInteractiveRuntime: fake, pane: "%9"})
	d.MarkInterrupted()
	if fake.killCount() != 1 {
		t.Fatalf("recovered pane kills=%d", fake.killCount())
	}
}
func TestReleaseRejectsActiveStates(t *testing.T) {
	for _, status := range []Status{StatusQueued, StatusRunning, StatusSettling} {
		t.Run(string(status), func(t *testing.T) {
			d, rt, id := releaseState(t, status)
			if _, err := d.Release(id); err == nil {
				t.Fatal("active release accepted")
			}
			if rt.killCount() != 0 {
				t.Fatal("active pane killed")
			}
		})
	}
}
func TestReleaseIdempotent(t *testing.T) {
	d, rt, id := releaseState(t, StatusIdle)
	if _, err := d.Release(id); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Release(id); err != nil {
		t.Fatal(err)
	}
	if rt.killCount() != 1 {
		t.Fatalf("kills=%d", rt.killCount())
	}
	rec, _ := d.Get(id)
	if rec.EndedAt.IsZero() {
		t.Fatal("release did not set EndedAt")
	}
	select {
	case <-d.runs[id].done:
	default:
		t.Fatal("release did not close done")
	}
}

func TestReleasePreservesConcurrentRecordAndBlocksHook(t *testing.T) {
	d, rt, id := releaseState(t, StatusIdle)
	rt.killHook = func() { _ = d.Report(id, hook(t, "UserPromptSubmit", "late")) }
	d.mu.Lock()
	d.runs[id].record.Text = "preserve"
	d.mu.Unlock()
	rec, err := d.Release(id)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Text != "preserve" || len(rec.Turns) != 0 {
		t.Fatalf("record=%+v", rec)
	}
}
func TestSweepCallerSessionGone(t *testing.T) {
	d, rt, id := releaseState(t, StatusIdle)
	gone := false
	rt.sessionAlive = &gone
	d.Sweep(time.Now())
	rec, err := d.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Status.Terminal() {
		t.Fatalf("status=%s", rec.Status)
	}
}

func TestSweepCleanupClearsPaneAndReappliesWindowLayout(t *testing.T) {
	d, rt, id := releaseState(t, StatusCanceled)
	d.runs[id].record.CallerWindowID = "@1"
	d.runs[id].killPending = true
	rt.alive = false
	d.Sweep(time.Now())
	rec, _ := d.Get(id)
	if rec.PaneID != "" {
		t.Fatalf("pane=%q", rec.PaneID)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.layouts) != 1 || rt.layouts[0] != "@1" {
		t.Fatalf("layouts=%v", rt.layouts)
	}
}

type blockingOpeningRuntime struct {
	*fakeInteractiveRuntime
	release <-chan struct{}
}

// lateOpeningErrorRuntime arms the opening turn, then holds its error until a
// later turn has been opened. It makes the async error ordering deterministic.
type lateOpeningErrorRuntime struct {
	*fakeInteractiveRuntime
	release  <-chan struct{}
	returned chan<- struct{}
}

func (r *lateOpeningErrorRuntime) InjectOpening(ctx context.Context, paneID, text string, arm func() error) error {
	if err := r.Inject(ctx, paneID, text, arm); err != nil {
		return err
	}
	<-r.release
	close(r.returned)
	return errors.New("late opening failure")
}

func (r *blockingOpeningRuntime) InjectOpening(ctx context.Context, paneID, text string, arm func() error) error {
	<-r.release
	return r.Inject(ctx, paneID, text, arm)
}

func TestInteractiveStartReturnsBeforeReady(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	release := make(chan struct{})
	rt := &blockingOpeningRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true}, release: release}
	d.SetInteractiveRuntime(rt)

	started := make(chan Started, 1)
	errs := make(chan error, 1)
	go func() {
		got, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive})
		started <- got
		errs <- err
	}()
	var got Started
	select {
	case got = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("Start blocked on opening readiness")
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	entry := d.Wait(context.Background(), []string{got.ID + "#1"}, time.Millisecond)[0]
	if entry.Outcome != "" || entry.Status != StatusQueued {
		t.Fatalf("opening entry = %+v, want open queued turn", entry)
	}
	close(release)
	deadline := time.After(time.Second)
	for rt.injectionCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("opening injection did not finish")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if err := d.Report(got.ID, hook(t, "UserPromptSubmit", "opening")); err != nil {
		t.Fatal(err)
	}
	rec, err := d.Get(got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Turns[0].Delivered {
		t.Fatalf("opening turn was not armed after injection: %#v", rec.Turns[0])
	}
}

func TestInteractiveOpeningIncludesDispatchPreamble(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	if _, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive}); err != nil {
		t.Fatal(err)
	}
	waitForInjection(t, rt)
	if got, want := rt.firstInjection(), dispatchPreamble+" hello"; got != want {
		t.Fatalf("opening prompt = %q, want %q", got, want)
	}
}

func TestInteractiveOpeningFailureSettlesAsync(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	release := make(chan struct{})
	rt := &blockingOpeningRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{injectErr: errors.New("not ready"), empty: true}, release: release}
	d.SetInteractiveRuntime(rt)
	got, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	entries := d.Wait(context.Background(), []string{got.ID + "#1"}, time.Second)
	if len(entries) != 1 || entries[0].Status != StatusFailed || entries[0].Outcome != TurnRejected {
		t.Fatalf("entries = %+v, want failed rejected opening", entries)
	}
}

func TestInteractiveLateOpeningFailureDoesNotSettleNewTurn(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	release := make(chan struct{})
	returned := make(chan struct{})
	rt := &lateOpeningErrorRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true}, release: release, returned: returned}
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "opening", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	waitForInjection(t, rt.fakeInteractiveRuntime)
	if err := d.Report(started.ID, hook(t, "UserPromptSubmit", "one")); err != nil {
		t.Fatal(err)
	}
	if err := d.Report(started.ID, hook(t, "Stop", "one")); err != nil {
		t.Fatal(err)
	}
	second, err := d.Send(context.Background(), started.ID, "second")
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("opening injection did not return its error")
	}
	deadline := time.After(time.Second)
	for {
		rec, getErr := d.Get(started.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if len(rec.Turns) == 2 && rec.Turns[1].Outcome == "" {
			if rec.Status == StatusFailed || rec.Turns[1].TurnID != second.TurnID || len(d.sem) != 1 {
				t.Fatalf("late error changed current turn: record=%+v slots=%d", rec, len(d.sem))
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("late opening error did not leave second turn open: record=%+v", rec)
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func hook(t *testing.T, event, turn string) HookReport {
	t.Helper()
	b, _ := json.Marshal(map[string]string{"hook_event_name": event, "turn_id": turn})
	return HookReport{EventID: event + turn, Payload: b}
}

func TestInteractiveReportMatching(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	got, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	waitForInjection(t, rt)
	rec, _ := d.Get(got.ID)
	tid := rec.Turns[0].TurnID
	if err := d.Report(got.ID, hook(t, "UserPromptSubmit", "abc")); err != nil {
		t.Fatal(err)
	}
	if err := d.Report(got.ID, hook(t, "Stop", "abc")); err != nil {
		t.Fatal(err)
	}
	rec, _ = d.Get(got.ID)
	if rec.Turns[0].Outcome != TurnFinished {
		t.Fatalf("outcome=%s", rec.Turns[0].Outcome)
	}
	// Event IDs deduplicate even after a state-changing report.
	_ = d.Report(got.ID, hook(t, "Stop", "abc"))
	rec, _ = d.Get(got.ID)
	if len(rec.Turns) != 1 {
		t.Fatal("duplicate changed turns")
	}
	_ = tid
}

func TestInteractiveWaitReturnsWhenTurnClosesBeforeSession(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	waitForInjection(t, rt)
	done := make(chan []Entry, 1)
	go func() { done <- d.Wait(context.Background(), []string{started.ID + "#1"}, time.Minute) }()
	_ = d.Report(started.ID, hook(t, "UserPromptSubmit", "one"))
	_ = d.Report(started.ID, hook(t, "Stop", "one"))
	select {
	case entries := <-done:
		if len(entries) != 1 || entries[0].Status != StatusIdle || entries[0].Outcome != TurnFinished {
			t.Fatalf("entries=%+v", entries)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait did not wake for closed turn")
	}
}

func TestInteractiveEntryStatusIsRunStatus(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	waitForInjection(t, rt)
	if err := d.Report(started.ID, hook(t, "UserPromptSubmit", "one")); err != nil {
		t.Fatal(err)
	}
	if err := d.Report(started.ID, hook(t, "Stop", "one")); err != nil {
		t.Fatal(err)
	}

	entry := d.Wait(context.Background(), []string{started.ID + "#1"}, time.Millisecond)[0]
	if entry.Status != StatusIdle || entry.Outcome != TurnFinished {
		t.Fatalf("entry=%+v, want idle run with finished turn", entry)
	}
}

func TestSweepDeadPaneSettlesWithinBound(t *testing.T) {
	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	d := NewDispatcher(newFakeRecorder())
	d.now = func() time.Time { return now }
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	waitForInjection(t, rt)
	if err := d.Report(started.ID, hook(t, "UserPromptSubmit", "one")); err != nil {
		t.Fatal(err)
	}
	if err := d.Report(started.ID, hook(t, "Stop", "one")); err != nil {
		t.Fatal(err)
	}
	rt.setAlive(false)
	d.Sweep(now)
	rec, _ := d.Get(started.ID)
	if rec.Status != StatusSettling {
		t.Fatalf("first sweep status=%s, want settling", rec.Status)
	}
	now = now.Add(finalReportGrace)
	d.Sweep(now)
	rec, _ = d.Get(started.ID)
	if rec.Status != StatusClosed {
		t.Fatalf("deadline sweep status=%s, want closed", rec.Status)
	}
}

func TestInteractiveSend(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Send(context.Background(), started.ID, "next"); err == nil {
		t.Fatal("send while queued accepted")
	}
	_ = d.Report(started.ID, hook(t, "UserPromptSubmit", "a"))
	_ = d.Report(started.ID, hook(t, "Stop", "a"))
	if _, err := d.Send(context.Background(), started.ID, "bad\ttext"); err == nil {
		t.Fatal("control character accepted")
	}
	sent, err := d.Send(context.Background(), started.ID, "next")
	if err != nil {
		t.Fatal(err)
	}
	if sent.TurnID == "" {
		t.Fatal("missing turn id")
	}
}

func TestInteractiveSlots(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	s, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	_ = d.Report(s.ID, hook(t, "UserPromptSubmit", "a"))
	_ = d.Report(s.ID, hook(t, "Interrupt", "a"))
	if len(d.sem) != 0 {
		t.Fatalf("slot leaked after interrupt: %d", len(d.sem))
	}
	if _, err := d.Send(context.Background(), s.ID, "again"); err != nil {
		t.Fatal(err)
	}
	if len(d.sem) != 1 {
		t.Fatalf("send did not hold slot: %d", len(d.sem))
	}
	_, _ = d.Cancel(s.ID)
	if len(d.sem) != 0 {
		t.Fatalf("slot leaked after settlement: %d", len(d.sem))
	}
}

func TestInteractiveWaitSnapshot(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	s, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	got := d.Wait(context.Background(), []string{s.ID}, time.Millisecond)[0]
	if got.TurnID == "" || got.Status != StatusQueued {
		t.Fatalf("queued wait=%+v", got)
	}
	b, _ := json.Marshal(map[string]string{"hook_event_name": "UserPromptSubmit", "turn_id": "a"})
	_ = d.Report(s.ID, HookReport{EventID: "new-submit", Payload: b})
	_ = d.Report(s.ID, hook(t, "Stop", "a"))
	got = d.Wait(context.Background(), []string{got.TurnID}, time.Millisecond)[0]
	if got.Outcome != TurnFinished {
		t.Fatalf("turn wait=%+v", got)
	}
}

func TestInteractiveSettlement(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	s, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Cancel(s.ID); err != nil {
		t.Fatal(err)
	}
	if rt.killCount() == 0 {
		t.Fatal("cancel did not kill pane before settlement")
	}
	rec, _ := d.Get(s.ID)
	if rec.Status != StatusCanceled {
		t.Fatalf("status=%s", rec.Status)
	}
	d.Sweep(time.Now())
}

func TestInteractiveClosedHarnessAndNoRuntime(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	if _, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive}); err == nil {
		t.Fatal("missing runtime accepted")
	}
	rt := &fakeInteractiveRuntime{arm: true, empty: true}
	d.SetInteractiveRuntime(rt)
	s, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	_ = d.Report(s.ID, hook(t, "UserPromptSubmit", "a"))
	_ = d.Report(s.ID, hook(t, "Stop", "a"))
	_ = d.Report(s.ID, hook(t, "Stop", "a"))
	// A replayed submit for a closed harness turn must not open a new turn.
	before, _ := d.Get(s.ID)
	b, _ := json.Marshal(map[string]string{"hook_event_name": "UserPromptSubmit", "turn_id": "a"})
	_ = d.Report(s.ID, HookReport{EventID: "new-submit", Payload: b})
	rec, _ := d.Get(s.ID)
	if len(rec.Turns) != len(before.Turns) || rec.Turns[len(rec.Turns)-1].Outcome != TurnFinished {
		t.Fatalf("replayed closed harness id changed turns: %+v", rec.Turns)
	}
}
