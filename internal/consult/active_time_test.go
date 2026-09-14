package consult

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"
)

func TestRecordActiveTimeIntervals(t *testing.T) {
	t0 := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	r := Record{}
	r.startActive(t0)
	r.startActive(t0.Add(time.Second))
	if got := r.LiveActiveSeconds(t0.Add(5 * time.Second)); got != 5 {
		t.Fatalf("live active = %v, want 5", got)
	}
	r.foldActive(t0.Add(7 * time.Second))
	r.foldActive(t0.Add(20 * time.Second))
	if r.ActiveSeconds != 7 || r.RunningSince != nil {
		t.Fatalf("folded record = %+v, want 7 seconds and stopped", r)
	}
	r.startActive(t0.Add(30 * time.Second))
	r.foldActive(t0.Add(33 * time.Second))
	if r.ActiveSeconds != 10 {
		t.Fatalf("accumulated active = %v, want 10", r.ActiveSeconds)
	}
}

func TestRecordActiveTimeJSONAndLegacyDefaults(t *testing.T) {
	t0 := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	b, err := json.Marshal(Record{ID: "x", ActiveSeconds: 3.5, RunningSince: &t0})
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip Record
	if err := json.Unmarshal(b, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if roundtrip.ActiveSeconds != 3.5 || roundtrip.RunningSince == nil || !roundtrip.RunningSince.Equal(t0) {
		t.Fatalf("roundtrip = %+v", roundtrip)
	}
	var legacy Record
	if err := json.Unmarshal([]byte(`{"id":"old"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.ActiveSeconds != 0 || legacy.RunningSince != nil {
		t.Fatalf("legacy defaults = %+v", legacy)
	}
}

func TestInteractiveActiveTimeStartsOnDeliveryAndFreezesOnIdle(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	d := NewDispatcher(nil)
	d.now = func() time.Time { return now }
	s := &runState{
		record: Record{ID: "d-x", Mode: ModeInteractive, Status: StatusQueued},
		handle: nopHandle{}, done: make(chan struct{}),
		armedTurn: "d-x#1", armedUntil: now.Add(time.Minute),
		pendingCloses: map[string]pendingClose{},
		closedHarness: map[string]bool{},
	}
	s.record.Turns = []Turn{{TurnID: "d-x#1", Source: TurnSourceOrchestrator}}
	d.mu.Lock()
	d.runs[s.record.ID] = s
	d.mu.Unlock()
	if s.record.RunningSince != nil {
		t.Fatal("queued turn started active time")
	}
	if err := d.Report(s.record.ID, hook(t, "UserPromptSubmit", "one")); err != nil {
		t.Fatal(err)
	}
	if s.record.RunningSince == nil || !s.record.RunningSince.Equal(now) {
		t.Fatalf("delivery did not start active time: %+v", s.record)
	}
	now = now.Add(4 * time.Second)
	if err := d.Report(s.record.ID, hook(t, "Stop", "one")); err != nil {
		t.Fatal(err)
	}
	if s.record.ActiveSeconds != 4 || s.record.RunningSince != nil || s.record.Status != StatusIdle {
		t.Fatalf("idle accounting = %+v", s.record)
	}
}

func TestInteractiveUserTurnStartsAndSettlementFreezes(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	d := NewDispatcher(nil)
	d.now = func() time.Time { return now }
	s := &runState{record: Record{ID: "d-x", Mode: ModeInteractive, Status: StatusIdle}, handle: nopHandle{}, done: make(chan struct{})}
	d.mu.Lock()
	d.openTurnLocked(s, TurnSourceUser, "", false)
	d.mu.Unlock()
	if s.record.RunningSince == nil {
		t.Fatal("user turn did not start active time")
	}
	now = now.Add(6 * time.Second)
	d.mu.Lock()
	d.beginSettlementLocked(s, StatusClosed, time.Minute)
	d.mu.Unlock()
	if s.record.ActiveSeconds != 6 || s.record.RunningSince != nil {
		t.Fatalf("settlement accounting = %+v", s.record)
	}
}

func TestHeadlessActiveTimeStartsAfterProcessStartAndFoldsAtCompletion(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	d := NewDispatcher(nil)
	d.now = func() time.Time { return now }
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", `sleep 0.2; printf '%s\n' '{"type":"result","result":"ok","is_error":false}'`)
	}
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "x", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		rec, _ := d.Get(started.ID)
		if rec.Status == StatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("process never reached running")
		}
		time.Sleep(time.Millisecond)
	}
	now = now.Add(5 * time.Second)
	entries := d.Wait(context.Background(), []string{started.ID}, time.Second)
	if len(entries) != 1 || entries[0].Status != StatusDone {
		t.Fatalf("entries = %+v", entries)
	}
	rec, _ := d.Get(started.ID)
	if rec.ActiveSeconds != 5 || rec.RunningSince != nil {
		t.Fatalf("completion accounting = %+v", rec)
	}
}

func TestHeadlessFailedStartDoesNotCountActiveTime(t *testing.T) {
	d := NewDispatcher(nil)
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/definitely/not/a/program")
	}
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "x", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{started.ID}, time.Second)
	rec, _ := d.Get(started.ID)
	if rec.Status != StatusFailed || rec.ActiveSeconds != 0 || rec.RunningSince != nil {
		t.Fatalf("failed-start accounting = %+v", rec)
	}
}

func TestFileRecorderClosePreservesAccountingBoundary(t *testing.T) {
	r, dir := newTestRecorder(t)
	t0 := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	now := t0.Add(20 * time.Second)
	r.Now = func() time.Time { return now }
	h, err := r.Open(Record{ID: "d-boundary", Status: StatusRunning, StartedAt: t0, RunningSince: &t0})
	if err != nil {
		t.Fatal(err)
	}
	boundary := t0.Add(4 * time.Second)
	rec := Record{ID: "d-boundary", Status: StatusCanceled, StartedAt: t0, EndedAt: boundary, ActiveSeconds: 4}
	if err := h.(recordHandle).SetRecord(rec); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(StatusCanceled, context.Canceled); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := h.Close(StatusFailed, context.DeadlineExceeded); err != nil {
		t.Fatal(err)
	}
	got, err := readRecord(dir + "/d-boundary.json")
	if err != nil {
		t.Fatal(err)
	}
	if !got.EndedAt.Equal(boundary) || got.ActiveSeconds != 4 || got.Status != StatusCanceled {
		t.Fatalf("persisted boundary = %+v", got)
	}
}

func TestHeadlessCancelPreservesOneBoundaryInMemoryAndOnDisk(t *testing.T) {
	fr, dir := newTestRecorder(t)
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	fr.Now = func() time.Time { return now }
	d := NewDispatcher(fr)
	d.now = func() time.Time { return now }
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "sleep 30")
	}
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "x", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		rec, _ := d.Get(started.ID)
		if rec.Status == StatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("process never reached running")
		}
		time.Sleep(time.Millisecond)
	}
	now = now.Add(4 * time.Second)
	canceled, err := d.Cancel(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	d.waitDone(started.ID)
	inMemory, _ := d.Get(started.ID)
	onDisk, err := readRecord(dir + "/" + started.ID + ".json")
	if err != nil {
		t.Fatal(err)
	}
	for label, got := range map[string]Record{"cancel result": canceled, "memory": inMemory, "disk": onDisk} {
		if got.Status != StatusCanceled || got.ActiveSeconds != 4 || got.RunningSince != nil || !got.EndedAt.Equal(now) {
			t.Errorf("%s boundary = %+v", label, got)
		}
	}
}

func TestMarkInterruptedFoldsPersistedRunningInterval(t *testing.T) {
	fr, dir := newTestRecorder(t)
	t0 := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	h, err := fr.Open(Record{ID: "d-restart", Status: StatusRunning, StartedAt: t0, ActiveSeconds: 2, RunningSince: &t0})
	if err != nil {
		t.Fatal(err)
	}
	if stream, ok := h.(*fileHandle); ok {
		_ = stream.stream.Close()
	}
	d := NewDispatcher(fr)
	markedAt := t0.Add(5 * time.Second)
	d.now = func() time.Time { return markedAt }
	d.MarkInterrupted()
	got, err := readRecord(dir + "/d-restart.json")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusFailed || got.ActiveSeconds != 7 || got.RunningSince != nil || !got.EndedAt.Equal(markedAt) {
		t.Fatalf("restart accounting = %+v", got)
	}
}

func TestInteractiveActiveTimeSequence(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	d := NewDispatcher(nil)
	d.now = func() time.Time { return now }
	s := &runState{record: Record{ID: "d-seq", Mode: ModeInteractive, Status: StatusQueued}, handle: nopHandle{}, done: make(chan struct{}), pendingCloses: map[string]pendingClose{}, closedHarness: map[string]bool{}}
	d.mu.Lock()
	d.runs[s.record.ID] = s
	t1 := d.openTurnLocked(s, TurnSourceOrchestrator, "one", false)
	t1.Delivered = true
	t1.HarnessTurnID = "one"
	s.record.Status = StatusRunning
	s.record.startActive(now)
	d.mu.Unlock()

	now = now.Add(3 * time.Second)
	_ = d.Report(s.record.ID, hook(t, "Stop", "one"))
	now = now.Add(7 * time.Second) // idle gap is excluded
	d.mu.Lock()
	t2 := d.openTurnLocked(s, TurnSourceUser, "", false)
	t2.HarnessTurnID = "two"
	d.mu.Unlock()
	now = now.Add(2 * time.Second)
	_ = d.Report(s.record.ID, hook(t, "Stop", "two"))
	_ = d.Report(s.record.ID, hook(t, "Stop", "two")) // duplicate cannot fold twice
	if s.record.ActiveSeconds != 5 || s.record.RunningSince != nil {
		t.Fatalf("two-interval accounting = %+v", s.record)
	}
}

func TestInteractiveReplaySubmitForClosedHarnessTurnDoesNotRestartActiveTime(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	d := NewDispatcher(nil)
	d.now = func() time.Time { return now }
	s := &runState{record: Record{ID: "d-replay", Mode: ModeInteractive, Status: StatusRunning}, handle: nopHandle{}, done: make(chan struct{}), pendingCloses: map[string]pendingClose{}, closedHarness: map[string]bool{}}
	d.mu.Lock()
	d.runs[s.record.ID] = s
	turn := d.openTurnLocked(s, TurnSourceUser, "", false)
	turn.HarnessTurnID = "closed"
	d.mu.Unlock()
	_ = d.Report(s.record.ID, hook(t, "Stop", "closed"))
	now = now.Add(10 * time.Second)
	_ = d.Report(s.record.ID, hook(t, "UserPromptSubmit", "closed"))
	now = now.Add(10 * time.Second)
	if got := s.record.ActiveSeconds; got != 0 {
		t.Fatalf("active seconds = %v, want 0", got)
	}
	if s.record.RunningSince != nil {
		t.Fatalf("replayed submit restarted active interval: %+v", s.record)
	}
}

func TestInteractiveAccountingReorderedRejectAndDefensiveSettlement(t *testing.T) {
	t0 := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		run  func(*Dispatcher, *runState, *time.Time)
		want float64
	}{
		{"undelivered rejection", func(d *Dispatcher, s *runState, now *time.Time) {
			d.mu.Lock()
			turn := d.openTurnLocked(s, TurnSourceOrchestrator, "", false)
			d.closeTurnLocked(s, turn.TurnID, TurnRejected, "")
			d.mu.Unlock()
		}, 0},
		{"reordered close then delivery", func(d *Dispatcher, s *runState, now *time.Time) {
			d.mu.Lock()
			turn := d.openTurnLocked(s, TurnSourceOrchestrator, "", false)
			turn.HarnessTurnID = "late"
			d.closeTurnLocked(s, turn.TurnID, TurnFinished, "")
			d.mu.Unlock()
			_ = d.Report(s.record.ID, hook(t, "UserPromptSubmit", "late"))
			d.mu.Lock()
			d.beginSettlementLocked(s, StatusClosed, 0)
			d.mu.Unlock()
		}, 0},
		{"finish defensively folds", func(d *Dispatcher, s *runState, now *time.Time) {
			d.mu.Lock()
			turn := d.openTurnLocked(s, TurnSourceUser, "", false)
			turn.HarnessTurnID = "x"
			d.mu.Unlock()
			*now = now.Add(4 * time.Second)
			d.mu.Lock()
			d.finishInteractiveLocked(s, StatusCanceled)
			d.mu.Unlock()
		}, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := t0
			d := NewDispatcher(nil)
			d.now = func() time.Time { return now }
			s := &runState{record: Record{ID: "d-edge", Mode: ModeInteractive, Status: StatusIdle}, handle: nopHandle{}, done: make(chan struct{}), pendingCloses: map[string]pendingClose{}, closedHarness: map[string]bool{}}
			d.mu.Lock()
			d.runs[s.record.ID] = s
			d.mu.Unlock()
			tt.run(d, s, &now)
			if s.record.ActiveSeconds != tt.want || s.record.RunningSince != nil {
				t.Fatalf("accounting = %+v, want %v", s.record, tt.want)
			}
		})
	}
}

func TestInteractiveTransitionsUseOneClockBoundary(t *testing.T) {
	t0 := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	t.Run("close", func(t *testing.T) {
		now, calls := t0, 0
		d := NewDispatcher(nil)
		d.now = func() time.Time { got := now.Add(time.Duration(calls) * time.Second); calls++; return got }
		s := &runState{record: Record{ID: "x", Status: StatusRunning, RunningSince: &t0, Turns: []Turn{{TurnID: "x#1", Source: TurnSourceUser}}}, handle: nopHandle{}}
		d.mu.Lock()
		d.closeTurnLocked(s, "x#1", TurnFinished, "")
		d.mu.Unlock()
		if !s.record.Turns[0].EndedAt.Equal(t0) || s.record.ActiveSeconds != 0 {
			t.Fatalf("close used inconsistent boundaries: %+v", s.record)
		}
	})
	t.Run("settlement", func(t *testing.T) {
		calls := 0
		d := NewDispatcher(nil)
		d.now = func() time.Time { got := t0.Add(time.Duration(calls) * time.Second); calls++; return got }
		s := &runState{record: Record{ID: "x", Status: StatusRunning, RunningSince: &t0}, handle: nopHandle{}}
		d.mu.Lock()
		d.beginSettlementLocked(s, StatusClosed, time.Minute)
		d.mu.Unlock()
		if s.record.ActiveSeconds != 0 || !s.settleDeadline.Equal(t0.Add(time.Minute)) {
			t.Fatalf("settlement used inconsistent boundaries: record=%+v deadline=%v", s.record, s.settleDeadline)
		}
	})
}

func TestRecordActiveTimeIgnoresBackwardClock(t *testing.T) {
	t0 := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	r := Record{ActiveSeconds: 2}
	r.startActive(t0)
	r.foldActive(t0.Add(-time.Second))
	if r.ActiveSeconds != 2 || r.RunningSince != nil {
		t.Fatalf("record = %+v, want unchanged accumulated time and stopped", r)
	}
}

func TestStateRecordDeepCopiesRunningSince(t *testing.T) {
	t0 := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	d := NewDispatcher(nil)
	s := &runState{record: Record{RunningSince: &t0}}
	d.mu.Lock()
	d.runs["x"] = s
	d.mu.Unlock()

	snapshot := d.stateRecord(s)
	*snapshot.RunningSince = snapshot.RunningSince.Add(time.Hour)
	if !s.record.RunningSince.Equal(t0) {
		t.Fatalf("snapshot mutated authoritative timestamp: %v", s.record.RunningSince)
	}
}
