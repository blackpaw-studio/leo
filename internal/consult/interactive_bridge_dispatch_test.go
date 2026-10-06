package consult

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// bridgedFakeRuntime is a fake runtime whose launches the bridge carries:
// await answers AwaitOpening, frame prefixes follow-ups.
type bridgedFakeRuntime struct {
	*fakeInteractiveRuntime
	bridges bool

	mu         sync.Mutex
	awaitCalls int
	await      func() (handled, paste bool, err error)
	frame      string
}

func (r *bridgedFakeRuntime) BridgesOpening(context.Context, string) bool { return r.bridges }

func (r *bridgedFakeRuntime) AwaitOpening(context.Context, string) (bool, bool, error) {
	r.mu.Lock()
	r.awaitCalls++
	await := r.await
	r.mu.Unlock()
	if await == nil {
		return true, false, nil
	}
	return await()
}

func (r *bridgedFakeRuntime) FrameMessage(_ string, message string) string { return r.frame + message }

func (r *bridgedFakeRuntime) awaited() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.awaitCalls
}

func waitForAwait(t *testing.T, r *bridgedFakeRuntime) {
	t.Helper()
	deadline := time.After(time.Second)
	for r.awaited() == 0 {
		select {
		case <-deadline:
			t.Fatal("AwaitOpening never ran")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

// A bridged opening, even one too large for argv, is submitted by claude
// itself: turn 1 is armed before Launch and nothing is pasted.
func TestInteractiveBridgedOpeningIsArmedBeforeLaunchAndNeverPasted(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &bridgedFakeRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true}, bridges: true}
	var armedAtLaunch bool
	rt.launchHook = func() {
		id := findSoleRunID(t, d)
		d.mu.Lock()
		armedAtLaunch = d.runs[id].armedTurn != ""
		d.mu.Unlock()
	}
	d.SetInteractiveRuntime(rt)
	big := strings.Repeat("a", claudeArgvPromptLimit+1)
	if _, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: big, Cwd: t.TempDir(), Mode: ModeInteractive}); err != nil {
		t.Fatal(err)
	}
	waitForAwait(t, rt)
	if !armedAtLaunch {
		t.Fatal("bridged opening was not armed before Launch")
	}
	time.Sleep(20 * time.Millisecond)
	if n := rt.injectionCount(); n != 0 {
		t.Fatalf("bridged opening was pasted %d time(s)", n)
	}
}

// When the bridge never connects and the legacy relaunch cannot carry the
// opening on argv, the dispatcher pastes it as before.
func TestInteractiveBridgedOpeningFallbackPastes(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &bridgedFakeRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true}, bridges: true}
	rt.await = func() (bool, bool, error) { return true, true, nil }
	d.SetInteractiveRuntime(rt)
	big := strings.Repeat("a", claudeArgvPromptLimit+1)
	if _, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: big, Cwd: t.TempDir(), Mode: ModeInteractive}); err != nil {
		t.Fatal(err)
	}
	waitForInjection(t, rt.fakeInteractiveRuntime)
	if got := rt.firstInjection(); got != dispatchPreamble+" "+big {
		t.Fatal("fallen-back opening was not pasted")
	}
}

func TestInteractiveBridgedOpeningRelaunchFailureFailsTheRun(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &bridgedFakeRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true}, bridges: true}
	rt.await = func() (bool, bool, error) { return true, false, errors.New("relaunching without the leo bridge: boom") }
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	entry := d.Wait(context.Background(), []string{started.ID + "#1"}, time.Second)[0]
	if entry.Status != StatusFailed || entry.Outcome != TurnRejected {
		t.Fatalf("entry = %+v, want a failed run with turn 1 rejected", entry)
	}
}

// A runtime that does not carry this launch over the bridge leaves the
// existing opening paths alone.
func TestInteractiveUnhandledAwaitKeepsTheLegacyOpening(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &bridgedFakeRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true}}
	rt.await = func() (bool, bool, error) { return false, false, nil }
	d.SetInteractiveRuntime(rt)
	if _, err := d.Start(context.Background(), testConfig(), Request{Template: "codex", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive}); err != nil {
		t.Fatal(err)
	}
	waitForInjection(t, rt.fakeInteractiveRuntime)
	if got := rt.firstInjection(); got != dispatchPreamble+" hello" {
		t.Fatalf("codex opening = %q", got)
	}
}

// A follow-up the bridge frames is recorded as framed, so a submit that
// arrives after the arm window still matches it by text.
func TestInteractiveSendRecordsTheFramedFollowUp(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	d := NewDispatcher(newFakeRecorder())
	d.now = func() time.Time { return now }
	rt := &bridgedFakeRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true}, bridges: true, frame: "From orch via leo:\n\n"}
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	waitForAwait(t, rt)
	waitForArmed(t, d, started.ID)
	for i, ev := range []string{"UserPromptSubmit", "Stop"} {
		if err := d.Report(started.ID, claudeHook(t, "open-"+ev, ev, dispatchPreamble+" hello")); err != nil {
			t.Fatalf("report %d: %v", i, err)
		}
	}
	res, err := d.Send(context.Background(), started.ID, "next step")
	if err != nil {
		t.Fatal(err)
	}
	framed := "From orch via leo:\n\nnext step"
	if got := rt.injected[len(rt.injected)-1]; got != framed {
		t.Fatalf("injected %q, want %q", got, framed)
	}
	now = now.Add(ackTimeout + time.Minute)
	if err := d.Report(started.ID, claudeHook(t, "late-submit", "UserPromptSubmit", framed)); err != nil {
		t.Fatal(err)
	}
	rec, err := d.Get(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn := turnByID(rec, res.TurnID)
	if !turn.Delivered || rec.Steered {
		t.Fatalf("late framed submit: turn %+v steered=%v", turn, rec.Steered)
	}
}

// A bridged dispatch's cost is its claude session's running total, as the
// mod reports it with each completed turn: each report replaces the last.
// Token counts are not reported, so the usage is marked incomplete.
func TestApplyBridgeUsageRecordsTheSessionCost(t *testing.T) {
	d := NewDispatcher(newFakeRecorder())
	rt := &bridgedFakeRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true}, bridges: true}
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	cost := func() (*float64, bool) {
		t.Helper()
		rec, err := d.Get(started.ID)
		if err != nil {
			t.Fatal(err)
		}
		return rec.CostUSD, rec.UsageIncomplete
	}
	d.ApplyBridgeUsage(started.ID, []byte(`{"startedAt":1,"context":{"tokens":900,"window":200000},"rateLimits":[],"cost":{"usd":0.42}}`))
	if got, partial := cost(); got == nil || *got != 0.42 || !partial {
		t.Fatalf("cost = %v partial=%v, want 0.42 partial", got, partial)
	}
	d.ApplyBridgeUsage(started.ID, []byte(`{"cost":{"usd":0.5}}`))
	if got, _ := cost(); got == nil || *got != 0.5 {
		t.Fatalf("cost = %v, want the running total 0.5", got)
	}
	for _, junk := range []string{`{"context":{}}`, `not json`, `{"cost":{"usd":-1}}`, `{"cost":{"usd":"x"}}`} {
		d.ApplyBridgeUsage(started.ID, []byte(junk))
		if got, _ := cost(); got == nil || *got != 0.5 {
			t.Fatalf("after %s cost = %v, want 0.5 kept", junk, got)
		}
	}
	d.ApplyBridgeUsage("d-unknown", []byte(`{"cost":{"usd":1}}`))
}
