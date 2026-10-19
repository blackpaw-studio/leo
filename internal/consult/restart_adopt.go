package consult

import (
	"fmt"
	"os"
)

// settleRestarted ends an interactive run the previous daemon left live, as
// "daemon restarted": its open turns are lost, the caller is sent the close
// notice, and a pane still standing is killed.
func (d *Dispatcher) settleRestarted(rec Record) {
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
