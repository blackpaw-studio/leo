package consult

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

// adoptionRuntime is a runtime that can take over a dispatch pane a previous
// daemon launched (see TmuxInteractiveRuntime).
type adoptionRuntime interface {
	ProbeAdoption(pane, id string) (AdoptProbe, error)
	AdoptPane(pane, harnessName, placement string)
	AdoptBridge(id, pane, caller, launch string) error
	PaneSnapshot(pane string) (screen string, empty bool, err error)
}

// Why a run the previous daemon left live is settled rather than adopted.
const (
	settleNoRuntime   = "no runtime"
	settleNoPane      = "no pane"
	settleUncertain   = "pane uncertain"
	settleAbsent      = "pane absent"
	settleNotOurs     = "pane not ours"
	settleDead        = "pane dead"
	settleUndelivered = "turn undelivered"
	settleSettling    = "run settling"
	settleTimeout     = "adopt timeout"
)

// adoptIdleGrace is how long an adopted run's composer must sit empty, with
// no report, before its working turn is taken to have finished while the
// daemon was down: long enough for the reports the bridge mod retries across
// the restart to land first.
const adoptIdleGrace = 15 * time.Second

// adoptNoResultText is the result of a turn that finished while the daemon
// was down when its transcript holds no final reply.
const adoptNoResultText = "finished while the daemon was down; no result recovered"

// adoptWaitLostText is the result of a turn that may have been paused on
// background work across a restart and that nothing reported on after it.
const adoptWaitLostText = "may have been waiting on background work when the daemon restarted; nothing reported how it ended"

// restoreInteractive adopts an interactive run the previous daemon left
// live, when its pane survived the restart and nothing it was doing lived
// only in that daemon; any other run is settled as "daemon restarted".
func (d *Dispatcher) restoreInteractive(rec Record) {
	rt, ok := d.interactiveRuntime.(adoptionRuntime)
	switch {
	case !ok:
		d.settleRestarted(rec, settleNoRuntime)
		return
	case rec.PaneID == "":
		d.settleRestarted(rec, settleNoPane)
		return
	case rec.Status == StatusSettling:
		// Its settle target, deadline and close reason lived only in the
		// old daemon: finish it as a restart, as before adoption.
		d.settleRestarted(rec, settleSettling)
		return
	}
	probe, err := rt.ProbeAdoption(rec.PaneID, rec.ID)
	if reason := adoptRefusal(rec, probe, err); reason != "" {
		if reason == settleNotOurs {
			// Whatever runs there now is not this dispatch's to close.
			rec.PaneID = ""
		}
		d.settleRestarted(rec, reason)
		return
	}
	d.adoptRestarted(rec, probe, rt)
}

// adoptRefusal is why rec cannot be adopted given its pane's probe, or ""
// when it can. A sent turn not yet seen to start was in flight in the dead
// daemon (a queued follow-up, or an opening or deliver on its bridge), so
// nothing would ever start it.
func adoptRefusal(rec Record, probe AdoptProbe, probeErr error) string {
	switch {
	case probeErr != nil:
		return settleUncertain
	case probe.Presence == PaneAbsent:
		return settleAbsent
	case !probe.Owned:
		return settleNotOurs
	case probe.Presence == PanePresentDead:
		return settleDead
	}
	for _, t := range rec.Turns {
		if t.Outcome == "" && t.Source == TurnSourceOrchestrator && !t.Delivered {
			return settleUndelivered
		}
	}
	return ""
}

// adoptRestarted takes rec over as a live run: same id, status and open
// turns, its slot and pane re-registered and, when its pane was tagged with
// a bridge launch, its bridge key re-opened for that launch. It stays
// adopting until it shows activity (see Sweep).
func (d *Dispatcher) adoptRestarted(rec Record, probe AdoptProbe, rt adoptionRuntime) {
	recorder, ok := d.recorder.(*FileRecorder)
	if !ok {
		return
	}
	now := d.now()
	waits := adoptedWaits(rec)
	// Background-task tracking (#277) restarts empty: the next Stop
	// repopulates it. A turn already waiting on background work still is.
	rec.BackgroundTasks, rec.OpenTools, rec.NeedsInput = nil, nil, nil
	rec.AdoptedAt, rec.HookActivity = now, now
	handle, err := recorder.Resume(rec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dispatch %s: reopening recording to adopt: %v\n", rec.ID, err)
		d.settleRestarted(rec, "recording unavailable")
		return
	}
	// The run's process is not this daemon's child: there is no context of
	// it to cancel.
	s := newRunState(rec, handle, make(chan struct{}), nil)
	s.lastHook, s.paneWant, s.wantPlacement = now, rec.ViewerKind, adoptedPlacement(rec.ViewerKind)
	rt.AdoptPane(rec.PaneID, rec.Harness, rec.ViewerKind)
	via := "hooks"
	if probe.Launch != "" {
		if err := rt.AdoptBridge(rec.ID, rec.PaneID, rec.Caller, probe.Launch); err != nil {
			fmt.Fprintf(os.Stderr, "dispatch %s: re-opening its leo bridge: %v\n", rec.ID, err)
		} else {
			via = "bridge"
		}
	}
	s.live.sessionID, s.live.transcriptPath = rec.SessionID, rec.TranscriptPath
	d.mu.Lock()
	timeout := d.adoptTimeout
	if timeout <= 0 {
		timeout = config.DefaultDispatchAdoptTimeout
	}
	s.adopting, s.adoptDeadline = true, now.Add(timeout)
	s.adoptWaits = waits
	s.adoptReconcile = hasWorkingTurnLocked(s) && len(waits) == 0
	for _, t := range s.record.Turns {
		if t.Outcome == "" && t.SlotHeld {
			d.slotsFor(s).Adopt()
		}
	}
	d.runs[rec.ID] = s
	d.recomputeStatusLocked(s, now)
	d.persistLocked(s, "status")
	d.nudgePaneLocked(s)
	d.mu.Unlock()
	if rec.Harness == "claude" {
		go d.watchLiveUsage(s)
	}
	fmt.Fprintf(os.Stderr, "dispatch %s: adopted (%s)\n", rec.ID, via)
}

// adoptedWaits is the open turns of rec a restart cannot read the end of
// from the transcript, or nil: when a turn was paused on background work, or
// the run had started background tasks (a Stop meanwhile would have paused
// it), every open turn, since the run's next Stop speaks for all of them.
func adoptedWaits(rec Record) map[string]bool {
	paused := len(rec.BackgroundTasks) > 0 || slices.ContainsFunc(rec.Turns, func(t Turn) bool {
		return t.Outcome == "" && t.Pending != nil
	})
	if !paused {
		return nil
	}
	waits := map[string]bool{}
	for _, t := range rec.Turns {
		if t.Outcome == "" {
			waits[t.TurnID] = true
		}
	}
	return waits
}

// openAdoptWaitsLocked is the turns s.adoptWaits still holds that are open.
func openAdoptWaitsLocked(s *runState) []string {
	var ids []string
	for _, t := range s.record.Turns {
		if t.Outcome == "" && s.adoptWaits[t.TurnID] {
			ids = append(ids, t.TurnID)
		}
	}
	return ids
}

// NoteBridgeHello records a hello from dispatch id's bridge mod: an adopted
// run whose mod reconnected is live. It says nothing of a turn that may
// have finished meanwhile, which only a report or the pane settles.
func (d *Dispatcher) NoteBridgeHello(id string) {
	_, s, err := d.lookup(id)
	if err != nil || s == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	s.adopting = false
}

// sweepAdopted watches the runs adopted across a restart until each shows
// it is live, settles a turn that finished while the daemon was down, loses
// the waits nothing reported on by the deadline, and settles a run still
// showing no sign of life at it. Tmux and transcript reads run without the
// lock.
func (d *Dispatcher) sweepAdopted(now time.Time) {
	d.mu.Lock()
	rt, ok := d.interactiveRuntime.(adoptionRuntime)
	var watched []*runState
	for _, s := range d.runs {
		if ok && watchingAdoptedLocked(s) {
			watched = append(watched, s)
		}
	}
	d.mu.Unlock()
	for _, s := range watched {
		d.sweepAdoptedRun(s, rt, now)
	}
}

func watchingAdoptedLocked(s *runState) bool {
	return (s.adopting || s.adoptReconcile || len(openAdoptWaitsLocked(s)) > 0) && s.record.PaneID != "" && !s.releasing &&
		!s.record.Status.Terminal() && s.record.Status != StatusSettling
}

func (d *Dispatcher) sweepAdoptedRun(s *runState, rt adoptionRuntime, now time.Time) {
	d.mu.Lock()
	pane := s.record.PaneID
	d.mu.Unlock()
	screen, empty, err := rt.PaneSnapshot(pane)
	if err != nil {
		return // a pane that went away closes through Sweep's own probe
	}
	d.mu.Lock()
	if !watchingAdoptedLocked(s) || s.record.PaneID != pane {
		d.mu.Unlock()
		return
	}
	idle := d.observeAdoptedPaneLocked(s, now, screen, empty)
	due := idle && s.adoptReconcile
	path, readable := "", false
	if due {
		path, readable = d.liveTranscriptPathLocked(s)
	}
	since := workingSinceLocked(s)
	d.mu.Unlock()
	text := ""
	if readable {
		if text, err = lastAssistantTextSince(d.liveUsage.read, path, since); err != nil {
			fmt.Fprintf(os.Stderr, "dispatch %s: reading transcript for the adopted turn: %v\n", s.record.ID, err)
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !watchingAdoptedLocked(s) {
		return
	}
	if due && s.adoptReconcile {
		d.reconcileAdoptedLocked(s, text)
	}
	switch {
	case now.Before(s.adoptDeadline):
	case s.adopting:
		d.settleAdoptTimeoutLocked(s)
	default:
		s.adoptReconcile = false
		if idle {
			d.loseAdoptWaitsLocked(s)
		}
	}
}

// observeAdoptedPaneLocked folds one capture of an adopted run's pane into
// its adoption, and reports whether the run is idle. A screen that changes
// while the composer is busy is the turn at work; an empty composer that
// stays so for adoptIdleGrace is a run with nothing in flight, so one that
// was working finished while the daemon was down.
func (d *Dispatcher) observeAdoptedPaneLocked(s *runState, now time.Time, screen string, empty bool) bool {
	changed := s.adoptSnapped && screen != s.adoptSnapshot
	s.adoptSnapshot, s.adoptSnapped = screen, true
	if !empty {
		s.adoptIdleSince = time.Time{}
		if changed {
			s.adopting, s.adoptReconcile = false, false
		}
		return false
	}
	if s.adoptIdleSince.IsZero() {
		s.adoptIdleSince = now
	}
	if now.Sub(s.adoptIdleSince) < adoptIdleGrace {
		return false
	}
	s.adopting = false
	return true
}

// workingSinceLocked is when the run's earliest working turn started.
func workingSinceLocked(s *runState) time.Time {
	var since time.Time
	for _, t := range s.record.Turns {
		if t.Outcome == "" && (t.Source == TurnSourceUser || t.Delivered) && (since.IsZero() || t.StartedAt.Before(since)) {
			since = t.StartedAt
		}
	}
	return since
}

// reconcileAdoptedLocked ends an adopted run's working turns, which finished
// while the daemon was down, with text read from the transcript: their
// final reply, or, with none, a lost outcome that says so.
func (d *Dispatcher) reconcileAdoptedLocked(s *runState, text string) {
	s.adopting, s.adoptReconcile = false, false
	if text == "" {
		d.closeWorkingLocked(s, TurnLost, adoptNoResultText)
		fmt.Fprintf(os.Stderr, "dispatch %s: adopted turn finished while the daemon was down; no result recovered\n", s.record.ID)
		return
	}
	d.closeWorkingLocked(s, TurnFinished, text)
	fmt.Fprintf(os.Stderr, "dispatch %s: adopted turn finished while the daemon was down; result read from its transcript\n", s.record.ID)
}

// loseAdoptWaitsLocked ends the turns an adopted run still holds for a
// report that never came: past the deadline with the composer idle, nothing
// is at work on them. Their slots free, their caller is told, and the run
// stays for follow-ups.
func (d *Dispatcher) loseAdoptWaitsLocked(s *runState) {
	ids := openAdoptWaitsLocked(s)
	s.adoptWaits = nil
	for _, id := range ids {
		d.closeTurnLocked(s, id, TurnLost, adoptWaitLostText)
	}
	if len(ids) > 0 {
		fmt.Fprintf(os.Stderr, "dispatch %s: adopted wait unreported by the deadline; %d turn(s) lost\n", s.record.ID, len(ids))
	}
}

// settleAdoptTimeoutLocked ends an adopted run that showed no sign of life
// by its deadline exactly as a run the restart could not adopt: lost turns,
// "daemon restarted", a close notice, and its pane killed.
func (d *Dispatcher) settleAdoptTimeoutLocked(s *runState) {
	s.adopting, s.adoptReconcile, s.adoptWaits = false, false, nil
	s.record.Error = closeReasonDaemonRestart
	s.closeReason = closeReasonDaemonRestart
	d.finishInteractiveLocked(s, StatusClosed)
	fmt.Fprintf(os.Stderr, "dispatch %s: settled: %s\n", s.record.ID, settleTimeout)
}

// adoptedPlacement is the viewer placement a pane found in viewerKind was
// steered to, for the placement watch to pick up from.
func adoptedPlacement(viewerKind string) string {
	switch viewerKind {
	case viewerBackground:
		return "background"
	case "window":
		return "window"
	}
	return "pane"
}

// settleRestarted ends an interactive run the previous daemon left live, as
// "daemon restarted": its open turns are lost, the caller is sent the close
// notice, and a pane still standing is killed. reason is why it was not
// adopted, for the log.
func (d *Dispatcher) settleRestarted(rec Record, reason string) {
	recorder, ok := d.recorder.(*FileRecorder)
	if !ok {
		return
	}
	markedAt := d.now()
	rec.Error, rec.EndedAt = closeReasonDaemonRestart, markedAt
	finished := false
	for i := range rec.Turns {
		if rec.Turns[i].Outcome == TurnFinished {
			finished = true
		}
		if rec.Turns[i].Outcome == "" {
			rec.Turns[i].Outcome = TurnLost
			rec.Turns[i].mergeWaitText("")
			rec.Turns[i].EndedAt = markedAt
			rec.Turns[i].SlotHeld = false
		}
		rec.Turns[i].Pending = nil
	}
	rec.PendingWork = nil
	if finished {
		rec.Status = StatusClosed
	} else {
		rec.Status = StatusFailed
	}
	rec.foldActive(markedAt)
	d.mu.Lock()
	d.addRestartCandidates(&rec)
	d.mu.Unlock()
	if rec.PaneID != "" && d.interactiveRuntime != nil {
		alive, certain := panePresent(d.interactiveRuntime, rec.PaneID)
		if !certain {
			if rec.Isolation == "worktree" {
				rec.WorktreeState = WorktreeKept
			}
		} else if alive {
			cleaned, killErr := d.closeRestartPane(rec)
			if killErr != nil {
				d.trackRestartKill(rec)
			} else {
				rec = cleaned
			}
		}
	}
	d.persistRestartSettle(recorder, rec)
	fmt.Fprintf(os.Stderr, "dispatch %s: settled: %s\n", rec.ID, reason)
}

// persistRestartSettle writes a run settled at startup, removes its worktree
// unless it must be kept, and requeues the notifications it still owes.
func (d *Dispatcher) persistRestartSettle(recorder *FileRecorder, rec Record) {
	if err := writeRecord(recorder.dir, rec); err != nil {
		fmt.Fprintf(os.Stderr, "dispatch %s: recording: %v\n", rec.ID, err)
	}
	if rec.Status.Terminal() && rec.WorktreeState != WorktreeKept {
		rec = d.cleanupWorktree(rec.ID)
	}
	d.restorePendingNotifications(rec)
}

// RunningInteractive returns the live interactive runs with a turn at
// work, oldest first: what a graceful restart lists as running dispatches,
// since adoption carries them across the restart.
func (d *Dispatcher) RunningInteractive() []Record {
	d.mu.Lock()
	defer d.mu.Unlock()
	var running []Record
	for _, s := range d.runs {
		rec := s.record
		if rec.Mode != ModeInteractive || rec.Status.Terminal() || rec.Status == StatusSettling || !hasWorkingTurnLocked(s) {
			continue
		}
		running = append(running, cloneRecord(rec))
	}
	sort.Slice(running, func(i, j int) bool {
		if !running[i].StartedAt.Equal(running[j].StartedAt) {
			return running[i].StartedAt.Before(running[j].StartedAt)
		}
		return running[i].ID < running[j].ID
	})
	return running
}
