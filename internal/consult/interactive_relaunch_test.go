package consult

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// generationHook is a claude hook report from session sid, with fields.
func generationHook(t *testing.T, eventID, event, sid string, fields map[string]string) HookReport {
	t.Helper()
	payload := map[string]string{"hook_event_name": event, "session_id": sid}
	for k, v := range fields {
		payload[k] = v
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return HookReport{EventID: eventID, Payload: b}
}

// relaunchedRun starts a bridged claude dispatch whose bridge never
// connects: AwaitOpening relaunches it the legacy way under session
// relaunchSession. The clock is the returned pointer.
func relaunchedRun(t *testing.T, relaunchSession string) (*Dispatcher, *bridgedFakeRuntime, string, *time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	d := NewDispatcher(newFakeRecorder())
	d.now = sharedClock(&now)
	rt := &bridgedFakeRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{arm: true, empty: true}, bridges: true}
	rt.await = func() (OpeningOutcome, error) {
		return OpeningOutcome{Handled: true, Relaunched: true, SessionID: relaunchSession}, nil
	}
	opened := make(chan struct{})
	d.afterOpeningInject = func() { close(opened) }
	d.SetInteractiveRuntime(rt)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "hello", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-opened:
	case <-time.After(time.Second):
		t.Fatal("opening never settled")
	}
	return d, rt, started.ID, &now
}

// A bridged launch whose mod never connected is relaunched in its pane
// (production d-0df348af1ca7): the killed claude's SessionEnd, delayed by
// load, must neither close the run nor kill the relaunched claude's pane,
// in whichever order it lands against the relaunch's own first submit,
// and the relaunch's submit, past the arm window, still starts turn 1.
func TestRelaunchedDispatchIgnoresTheKilledSessionsLateSessionEnd(t *testing.T) {
	const killed, relaunched = "2384ef45-a", "e0c21169-b"
	for _, staleFirst := range []bool{false, true} {
		name := "submit first"
		if staleFirst {
			name = "stale SessionEnd first"
		}
		t.Run(name, func(t *testing.T) {
			d, rt, id, now := relaunchedRun(t, relaunched)
			advance(now, 32*time.Second) // well past ackTimeout
			submit := generationHook(t, "b-submit", "UserPromptSubmit", relaunched, map[string]string{"prompt": dispatchPreamble + " hello", "transcript_path": "/t/" + relaunched + ".jsonl"})
			stale := generationHook(t, "a-end", "SessionEnd", killed, map[string]string{"reason": "other", "transcript_path": "/t/" + killed + ".jsonl"})
			order := []HookReport{submit, stale}
			if staleFirst {
				order = []HookReport{stale, submit}
			}
			for _, r := range order {
				if err := d.Report(id, r); err != nil {
					t.Fatal(err)
				}
			}
			// Past the grace a settle waits for, a stale close would have
			// finished the run and killed its pane by now.
			advance(now, finalReportGrace+time.Second)
			d.Sweep(*now)
			rec, err := d.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			if !turnByID(rec, id+"#1").Delivered {
				t.Fatalf("relaunch's late submit did not start turn 1: %+v", turnByID(rec, id+"#1"))
			}
			if rec.Status == StatusSettling || rec.Status.Terminal() {
				t.Fatalf("stale SessionEnd settled the run: status %s", rec.Status)
			}
			if rec.SessionID != relaunched {
				t.Fatalf("session = %q, want the relaunch's %q", rec.SessionID, relaunched)
			}
			if rec.TranscriptPath != "/t/"+relaunched+".jsonl" {
				t.Fatalf("transcript = %q, want the relaunch's", rec.TranscriptPath)
			}
			if n := rt.killCount(); n != 0 {
				t.Fatalf("relaunched pane killed %d time(s)", n)
			}
			advance(now, 8*time.Second)
			if err := d.Report(id, generationHook(t, "b-stop", "Stop", relaunched, map[string]string{"last_assistant_message": "all done"})); err != nil {
				t.Fatal(err)
			}
			entry := d.Wait(context.Background(), []string{id + "#1"}, time.Second)[0]
			if entry.Outcome != TurnFinished || entry.Text != "all done" {
				t.Fatalf("turn 1 = %+v, want finished with the relaunch's text", entry)
			}
		})
	}
}

// The relaunched session's own SessionEnd still closes the run, and a hook
// that names no session keeps today's handling.
func TestRelaunchedDispatchStillClosesOnItsOwnSessionEnd(t *testing.T) {
	for _, sid := range []string{"e0c21169-b", ""} {
		t.Run("session="+sid, func(t *testing.T) {
			d, _, id, _ := relaunchedRun(t, "e0c21169-b")
			if err := d.Report(id, generationHook(t, "end", "SessionEnd", sid, nil)); err != nil {
				t.Fatal(err)
			}
			rec, err := d.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			if rec.Status != StatusSettling {
				t.Fatalf("status = %s, want settling on the live session's end", rec.Status)
			}
		})
	}
}
