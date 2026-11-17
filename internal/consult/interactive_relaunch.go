package consult

import (
	"fmt"
	"os"
)

// bindRelaunchedSession records that run s's pane was relaunched without
// the bridge as a fresh claude pinned to session sid. The killed bridged
// claude still reports its SessionEnd, and under load that can land long
// after the relaunch is working: binding sid lets Report drop it, rather
// than close the run and kill the relaunch's pane. The relaunch's first
// hook then finds the run on its own session. An unpinned relaunch (one
// resuming the run's session, so sharing its id) binds nothing.
func (d *Dispatcher) bindRelaunchedSession(s *runState, sid string) {
	if sid == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.record.Status.Terminal() || s.reopenUnconfirmed {
		return
	}
	if old := s.record.SessionID; old != "" && old != sid {
		fmt.Fprintf(os.Stderr, "dispatch %s: relaunched as session %s; retiring session %s\n", s.record.ID, sid, old)
	}
	s.liveSession = sid
	s.record.SessionID = sid
	d.persistLocked(s, "")
}

// fromRetiredSessionLocked reports whether hook payload p comes from a
// session run s's pane no longer runs, logging the drop. It is checked
// before a report touches any state. A payload naming no session is never
// dropped.
func (d *Dispatcher) fromRetiredSessionLocked(s *runState, event string, p map[string]any) bool {
	sid := str(p, "session_id")
	if s.liveSession == "" || sid == "" || sid == s.liveSession {
		return false
	}
	fmt.Fprintf(os.Stderr, "dispatch %s: ignoring %s from retired session %s (the pane runs %s)\n", s.record.ID, event, sid, s.liveSession)
	return true
}
