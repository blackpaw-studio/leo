package consult

import (
	"fmt"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// resumableRetention is how long a parked run is kept for a follow-up
// before the retention sweep releases it (D1).
var resumableRetention = 7 * 24 * time.Hour

// retentionText is d in whole days when it is some, else as shortDuration
// words it.
func retentionText(d time.Duration) string {
	const day = 24 * time.Hour
	if d >= day && d%day == 0 {
		if d == day {
			return "1 day"
		}
		return fmt.Sprintf("%d days", d/day)
	}
	return shortDuration(d)
}

// Why a released run was released (Record.ReleaseReason).
const (
	ReleaseReasonManual    = "manual"
	ReleaseReasonRetention = "retention"
)

// parked reports a closed interactive run whose pane closed on a delivered
// result: the run keeps its session, cwd and worktree for a follow-up to
// reopen. Records written before Retained existed read as not parked.
func parked(rec Record) bool {
	return rec.Mode == ModeInteractive && rec.Status == StatusClosed && rec.Retained
}

// resumable reports a parked run a follow-up can reopen: its session is
// known, no earlier reopen found it unresumable, and its harness resumes an
// interactive session.
func resumable(rec Record) bool {
	return parked(rec) && notResumableReason(rec) == ""
}

// notResumableReason says why a parked run's session cannot be reopened, or
// "" when nothing in the record stops it.
func notResumableReason(rec Record) string {
	switch {
	case rec.SessionID == "":
		return "no session id was recorded"
	case rec.ResumeError != "":
		return rec.ResumeError
	case !harnessResumesInteractive(rec.Harness):
		return fmt.Sprintf("harness %q cannot resume an interactive session", rec.Harness)
	}
	return ""
}

// harnessResumesInteractive reports whether harnessName can relaunch an
// interactive dispatch on its earlier session (harness.InteractiveResumer):
// claude (--resume <id>) and codex (resume <id>). Opencode has no
// interactive dispatch.
func harnessResumesInteractive(harnessName string) bool {
	h, err := harness.Get(harnessName)
	if err != nil {
		return false
	}
	_, ok := h.(harness.InteractiveResumer)
	return ok
}
