package consult

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

type fakeInteractiveRuntime struct {
	pane     string
	alive    bool
	injected []string
	arm      bool
	empty    bool
	kill     int
}

func (r *fakeInteractiveRuntime) Launch(context.Context, LaunchRequest) (string, string, error) {
	if r.pane == "" {
		r.pane = "%1"
	}
	r.alive = true
	return r.pane, "w", nil
}
func (r *fakeInteractiveRuntime) Inject(_ context.Context, _ string, text string, arm func()) error {
	r.injected = append(r.injected, text)
	if r.arm {
		arm()
	}
	return nil
}
func (r *fakeInteractiveRuntime) Alive(string) bool         { return r.alive }
func (r *fakeInteractiveRuntime) Kill(string) error         { r.kill++; r.alive = false; return nil }
func (r *fakeInteractiveRuntime) ComposerEmpty(string) bool { return r.empty }
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
	done := make(chan []Entry, 1)
	go func() { done <- d.Wait(context.Background(), []string{started.ID + "#1"}, time.Second) }()
	_ = d.Report(started.ID, hook(t, "UserPromptSubmit", "one"))
	_ = d.Report(started.ID, hook(t, "Stop", "one"))
	select {
	case entries := <-done:
		if len(entries) != 1 || entries[0].Outcome != TurnFinished {
			t.Fatalf("entries=%+v", entries)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("wait did not wake for closed turn")
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
	if rt.kill == 0 {
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
	// A late duplicate close must not be buffered for a future submit using a.
	b, _ := json.Marshal(map[string]string{"hook_event_name": "UserPromptSubmit", "turn_id": "a"})
	_ = d.Report(s.ID, HookReport{EventID: "new-submit", Payload: b})
	rec, _ := d.Get(s.ID)
	if rec.Turns[len(rec.Turns)-1].Outcome != "" {
		t.Fatal("closed harness id applied to new turn")
	}
}
