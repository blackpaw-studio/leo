package consult

import (
	"encoding/json"
	"errors"
	"reflect"
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

// adoptingRuntime is a fake runtime that can adopt: its probe answers probe
// (or probeErr), and it records what it was asked to re-register.
type adoptingRuntime struct {
	*fakeInteractiveRuntime
	probe     AdoptProbe
	probeErr  error
	probed    []string
	panes     [][3]string
	bridges   [][4]string
	snapshot  string
	snapEmpty bool
}

func (r *adoptingRuntime) PaneSnapshot(string) (string, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.probe.Presence == PaneAbsent {
		return "", false, errors.New("can't find pane")
	}
	return r.snapshot, r.snapEmpty, nil
}

func (r *adoptingRuntime) setSnapshot(screen string, empty bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snapshot, r.snapEmpty = screen, empty
}

// setGone makes the pane vanish.
func (r *adoptingRuntime) setGone() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.probe.Presence, r.alive = PaneAbsent, false
}

func (r *adoptingRuntime) ProbeAdoption(pane, id string) (AdoptProbe, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.probed = append(r.probed, pane+" "+id)
	return r.probe, r.probeErr
}

// PanePresence agrees with the probe, as tmux would.
func (r *adoptingRuntime) PanePresence(string) (PanePresence, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.probe.Presence, r.probeErr
}

func (r *adoptingRuntime) AdoptPane(pane, harnessName, placement string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.panes = append(r.panes, [3]string{pane, harnessName, placement})
}

func (r *adoptingRuntime) AdoptBridge(id, pane, caller, launch string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bridges = append(r.bridges, [4]string{id, pane, caller, launch})
	return nil
}

var adoptNow = time.Date(2026, time.October, 19, 9, 5, 0, 0, time.UTC)

// workingRecord is a claude dispatch the previous daemon left mid-turn in
// pane %9.
func workingRecord() Record {
	rec := liveInteractiveRecord("d-adopt", Turn{TurnID: "d-adopt#1", Source: TurnSourceOrchestrator, StartedAt: adoptNow.Add(-6 * time.Minute), Delivered: true, SlotHeld: true, Text: "do it"})
	rec.Harness, rec.Caller, rec.ViewerKind = "claude", "orch", "split"
	return rec
}

func restartWith(t *testing.T, rec Record, rt InteractiveRuntime) (*Dispatcher, string) {
	t.Helper()
	stateDir := t.TempDir()
	restartRecord(t, stateDir, rec)
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.now = func() time.Time { return adoptNow }
	d.SetInteractiveRuntime(rt)
	d.MarkInterrupted()
	return d, stateDir
}

func ownedProbe(launch string) AdoptProbe {
	return AdoptProbe{Presence: PanePresentAlive, Owned: true, Launch: launch}
}

func TestMarkInterruptedAdoptsALiveOwnedPane(t *testing.T) {
	rt := &adoptingRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{alive: true}, probe: ownedProbe("tok_ABC")}
	rec := workingRecord()
	rec.BackgroundTasks = []BackgroundTask{{ID: "bg1"}}
	d, stateDir := restartWith(t, rec, rt)
	got := mustGet(t, d, "d-adopt")
	if got.Status != StatusRunning || got.Error != "" || got.Turns[0].Outcome != "" || !got.Turns[0].SlotHeld {
		t.Fatalf("adopted record = status %s error %q turn %+v; want the same running turn", got.Status, got.Error, got.Turns[0])
	}
	if !got.AdoptedAt.Equal(adoptNow) || len(got.Notifications) != 0 || len(got.BackgroundTasks) != 0 {
		t.Fatalf("adopted_at=%v notifications=%v background=%v", got.AdoptedAt, got.Notifications, got.BackgroundTasks)
	}
	if rt.killCount() != 0 || got.PaneID != "%9" {
		t.Fatalf("kills=%d pane=%q; the adopted pane must live on", rt.killCount(), got.PaneID)
	}
	if want := [][3]string{{"%9", "claude", "split"}}; !reflect.DeepEqual(rt.panes, want) {
		t.Fatalf("AdoptPane calls = %v, want %v", rt.panes, want)
	}
	if want := [][4]string{{"d-adopt", "%9", "orch", "tok_ABC"}}; !reflect.DeepEqual(rt.bridges, want) {
		t.Fatalf("AdoptBridge calls = %v, want %v", rt.bridges, want)
	}
	onDisk, err := LoadOne(stateDir, "d-adopt")
	if err != nil || !onDisk.AdoptedAt.Equal(adoptNow) || onDisk.Status != StatusRunning {
		t.Fatalf("persisted record = %+v, %v", onDisk, err)
	}
}

func TestMarkInterruptedAdoptsAnUntaggedPaneHookDriven(t *testing.T) {
	rt := &adoptingRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{alive: true}, probe: ownedProbe("")}
	d, _ := restartWith(t, workingRecord(), rt)
	if got := mustGet(t, d, "d-adopt"); got.Status != StatusRunning || got.AdoptedAt.IsZero() {
		t.Fatalf("status=%s adopted_at=%v", got.Status, got.AdoptedAt)
	}
	if len(rt.bridges) != 0 {
		t.Fatalf("re-opened a bridge for an untagged pane: %v", rt.bridges)
	}
}

func TestAdoptRefusal(t *testing.T) {
	undelivered := workingRecord()
	undelivered.Turns[0].Delivered = false
	userTurn := workingRecord()
	userTurn.Turns[0] = Turn{TurnID: "d-adopt#1", Source: TurnSourceUser}
	for _, tc := range []struct {
		name  string
		rec   Record
		probe AdoptProbe
		err   error
		want  string
	}{
		{"adoptable", workingRecord(), ownedProbe("tok"), nil, ""},
		{"user turn", userTurn, ownedProbe(""), nil, ""},
		{"probe failed", workingRecord(), AdoptProbe{}, errors.New("no server"), "pane uncertain"},
		{"absent", workingRecord(), AdoptProbe{Presence: PaneAbsent}, nil, "pane absent"},
		{"foreign", workingRecord(), AdoptProbe{Presence: PanePresentAlive}, nil, "pane not ours"},
		{"foreign and dead", workingRecord(), AdoptProbe{Presence: PanePresentDead}, nil, "pane not ours"},
		{"dead", workingRecord(), AdoptProbe{Presence: PanePresentDead, Owned: true}, nil, "pane dead"},
		{"undelivered", undelivered, ownedProbe("tok"), nil, "turn undelivered"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := adoptRefusal(tc.rec, tc.probe, tc.err); got != tc.want {
				t.Fatalf("adoptRefusal = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMarkInterruptedSettlesWhatItCannotAdopt(t *testing.T) {
	undelivered := workingRecord()
	undelivered.Turns[0].Delivered = false
	// A run in its final-report grace kept its settle target and deadline
	// only in the old daemon's memory: nothing is left to adopt.
	settling := workingRecord()
	settling.Status = StatusSettling
	for _, tc := range []struct {
		name     string
		rec      Record
		probe    AdoptProbe
		err      error
		wantKill bool
	}{
		{"absent", workingRecord(), AdoptProbe{Presence: PaneAbsent}, nil, false},
		{"dead", workingRecord(), AdoptProbe{Presence: PanePresentDead, Owned: true}, nil, true},
		{"not ours", workingRecord(), AdoptProbe{Presence: PanePresentAlive}, nil, false},
		{"uncertain", workingRecord(), AdoptProbe{}, errors.New("no server"), false},
		{"undelivered", undelivered, ownedProbe("tok"), nil, true},
		{"settling", settling, ownedProbe("tok"), nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &adoptingRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{alive: true}, probe: tc.probe, probeErr: tc.err}
			d, _ := restartWith(t, tc.rec, rt)
			d.Sweep(adoptNow.Add(time.Minute))
			got := mustGet(t, d, "d-adopt")
			if got.Status != StatusFailed || got.Error != "daemon restarted" || got.Turns[0].Outcome != TurnLost || !got.AdoptedAt.IsZero() {
				t.Fatalf("record = status %s error %q turn %+v adopted_at %v; want settled as today", got.Status, got.Error, got.Turns[0], got.AdoptedAt)
			}
			if _, ok := got.Notifications[closeNoticeKey("d-adopt")]; !ok {
				t.Fatal("no close notice")
			}
			if killed := rt.killCount() > 0; killed != tc.wantKill {
				t.Fatalf("killed=%v, want %v", killed, tc.wantKill)
			}
			if len(rt.panes) != 0 || len(rt.bridges) != 0 {
				t.Fatalf("re-registered a settled run: panes=%v bridges=%v", rt.panes, rt.bridges)
			}
		})
	}
}

func TestMarkInterruptedLeavesHeadlessRunsAsToday(t *testing.T) {
	rec := Record{ID: "d-head", Kind: "dispatch", Mode: ModeHeadless, Status: StatusRunning, StartedAt: adoptNow.Add(-time.Minute)}
	rt := &adoptingRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{alive: true}, probe: ownedProbe("tok")}
	d, _ := restartWith(t, rec, rt)
	got := mustGet(t, d, "d-head")
	if got.Status != StatusFailed || got.Error != "daemon restarted" || got.Turns[0].Outcome != TurnInterrupted || len(rt.probed) != 0 {
		t.Fatalf("headless record = %+v probed=%v", got, rt.probed)
	}
}

func stopReport(id, text string) HookReport {
	b, _ := json.Marshal(map[string]string{"hook_event_name": "Stop", "last_assistant_message": text})
	return HookReport{EventID: "stop-" + id, Payload: b}
}

// The adopted run is live again: its turn's Stop finishes it and the
// caller is told, as if the daemon had never gone away.
func TestAdoptedRunTakesItsStop(t *testing.T) {
	rt := &adoptingRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{alive: true}, probe: ownedProbe("")}
	d, _ := restartWith(t, workingRecord(), rt)
	if err := d.Report("d-adopt", stopReport("d-adopt", "ADOPT-OK")); err != nil {
		t.Fatal(err)
	}
	got := mustGet(t, d, "d-adopt")
	if got.Status != StatusIdle || got.Turns[0].Outcome != TurnFinished || got.Turns[0].Text != "ADOPT-OK" {
		t.Fatalf("status=%s turn=%+v; want the turn finished with its text", got.Status, got.Turns[0])
	}
	if n, ok := got.Notifications["d-adopt#1"]; !ok || n.Disposition != NotificationPending {
		t.Fatalf("turn notification = %+v (%v), want pending", n, ok)
	}
}

func TestAdoptedWorkingTurnHoldsASlot(t *testing.T) {
	rt := &adoptingRuntime{fakeInteractiveRuntime: &fakeInteractiveRuntime{alive: true}, probe: ownedProbe("")}
	stateDir := t.TempDir()
	restartRecord(t, stateDir, workingRecord())
	d := NewDispatcher(NewFileRecorder(stateDir))
	d.now = func() time.Time { return adoptNow }
	d.slots.SetMax(1)
	d.SetInteractiveRuntime(rt)
	d.MarkInterrupted()
	if d.slots.InUse() != 1 || d.slots.TryAcquire() {
		t.Fatalf("in use = %d; the adopted turn must hold the only slot", d.slots.InUse())
	}
	if err := d.Report("d-adopt", stopReport("d-adopt", "done")); err != nil {
		t.Fatal(err)
	}
	if d.slots.InUse() != 0 {
		t.Fatalf("in use = %d after the adopted turn ended", d.slots.InUse())
	}
}

// RunningInteractive is what a graceful restart lists as running
// dispatches: live interactive runs with a turn at work, oldest first.
func TestRunningInteractiveListsLiveWorkingRuns(t *testing.T) {
	d := NewDispatcher(nil)
	working := Turn{Source: TurnSourceOrchestrator, Delivered: true}
	add := func(rec Record, at time.Duration) {
		rec.StartedAt = adoptNow.Add(at)
		d.runs[rec.ID] = &runState{record: rec, handle: nopHandle{}, done: make(chan struct{})}
	}
	add(liveInteractiveRecord("d-late", working), -time.Minute)
	add(liveInteractiveRecord("d-early", Turn{Source: TurnSourceUser}), -time.Hour)
	add(liveInteractiveRecord("d-unsent", Turn{Source: TurnSourceOrchestrator}), -2*time.Minute)
	add(liveInteractiveRecord("d-idle", Turn{Source: TurnSourceOrchestrator, Delivered: true, Outcome: TurnFinished}), -3*time.Minute)
	settling := liveInteractiveRecord("d-settling", working)
	settling.Status = StatusSettling
	add(settling, -4*time.Minute)
	closed := liveInteractiveRecord("d-closed", working)
	closed.Status = StatusClosed
	add(closed, -5*time.Minute)
	headless := liveInteractiveRecord("d-headless")
	headless.Mode = ModeHeadless
	add(headless, -6*time.Minute)

	var ids []string
	for _, rec := range d.RunningInteractive() {
		ids = append(ids, rec.ID)
	}
	if want := []string{"d-early", "d-late"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("RunningInteractive = %v, want %v", ids, want)
	}
}
