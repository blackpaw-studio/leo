package consult

import (
	"fmt"
	"os"

	"github.com/blackpaw-studio/leo/internal/config"
)

// adoptionRuntime is a runtime that can take over a dispatch pane a previous
// daemon launched (see TmuxInteractiveRuntime).
type adoptionRuntime interface {
	ProbeAdoption(pane, id string) (AdoptProbe, error)
	AdoptPane(pane, harnessName, placement string)
	AdoptBridge(id, pane, caller, launch string) error
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
)

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
	d.mu.Lock()
	timeout := d.adoptTimeout
	if timeout <= 0 {
		timeout = config.DefaultDispatchAdoptTimeout
	}
	s.adopting, s.adoptDeadline = true, now.Add(timeout)
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
