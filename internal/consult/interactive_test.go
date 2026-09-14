package consult

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeInteractiveRuntime struct {
	mu        sync.Mutex
	pane      string
	alive     bool
	injected  []string
	arm       bool
	empty     bool
	kill      int
	injectErr error
}

func (r *fakeInteractiveRuntime) Launch(context.Context, LaunchRequest) (string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pane == "" {
		r.pane = "%1"
	}
	r.alive = true
	return r.pane, "w", nil
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
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kill++
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
	case <-time.After(300 * time.Millisecond):
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
	go func() { done <- d.Wait(context.Background(), []string{started.ID + "#1"}, time.Second) }()
	_ = d.Report(started.ID, hook(t, "UserPromptSubmit", "one"))
	_ = d.Report(started.ID, hook(t, "Stop", "one"))
	select {
	case entries := <-done:
		if len(entries) != 1 || entries[0].Status != StatusIdle || entries[0].Outcome != TurnFinished {
			t.Fatalf("entries=%+v", entries)
		}
	case <-time.After(300 * time.Millisecond):
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
