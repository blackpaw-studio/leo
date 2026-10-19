package consult

import (
	"testing"
	"time"
)

// restartRecord persists an interactive run as the previous daemon left it.
func restartRecord(t *testing.T, stateDir string, rec Record) {
	t.Helper()
	h, err := NewFileRecorder(stateDir).Open(rec)
	if err != nil {
		t.Fatal(err)
	}
	_ = h.Close(rec.Status, nil)
}

func liveInteractiveRecord(id string, turns ...Turn) Record {
	return Record{ID: id, Kind: "dispatch", Template: "worker", Mode: ModeInteractive, Status: StatusRunning, PaneID: "%9",
		Notify: true, CallerPaneID: "%1", CallerHarness: "claude", Turns: turns}
}

func TestMarkInterruptedSettlesInteractiveRun(t *testing.T) {
	started := time.Date(2026, time.October, 19, 9, 0, 0, 0, time.UTC)
	working := Turn{TurnID: "d-settle#2", Source: TurnSourceOrchestrator, StartedAt: started, Delivered: true, SlotHeld: true}
	for _, tc := range []struct {
		name  string
		turns []Turn
		want  Status
	}{
		{"no finished turn fails", []Turn{{TurnID: "d-settle#1", Source: TurnSourceOrchestrator, StartedAt: started, Delivered: true, SlotHeld: true}}, StatusFailed},
		{"a finished turn closes", []Turn{{TurnID: "d-settle#1", Source: TurnSourceOrchestrator, Outcome: TurnFinished, Delivered: true, Text: "first"}, working}, StatusClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			restartRecord(t, stateDir, liveInteractiveRecord("d-settle", tc.turns...))
			rt := &fakeInteractiveRuntime{alive: true}
			d := NewDispatcher(NewFileRecorder(stateDir))
			d.SetInteractiveRuntime(rt)
			d.MarkInterrupted()
			got, err := LoadOne(stateDir, "d-settle")
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.want || got.Error != "daemon restarted" || got.EndedAt.IsZero() {
				t.Fatalf("status=%s error=%q ended=%v, want %s / daemon restarted", got.Status, got.Error, got.EndedAt, tc.want)
			}
			open := got.Turns[len(got.Turns)-1]
			if open.Outcome != TurnLost || open.SlotHeld || open.EndedAt.IsZero() {
				t.Fatalf("open turn=%+v, want lost without a slot", open)
			}
			if n, ok := got.Notifications[closeNoticeKey("d-settle")]; !ok || n.Disposition != NotificationPending {
				t.Fatalf("close notice=%+v ok=%v, want pending", n, ok)
			}
			if rt.killCount() != 1 || got.PaneID != "" {
				t.Fatalf("kills=%d pane=%q, want the pane killed and cleared", rt.killCount(), got.PaneID)
			}
		})
	}
}
