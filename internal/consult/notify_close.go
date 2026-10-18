package consult

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const closeNoticeSuffix = "#closed"

// Reasons a run is closed on its own, as its close notice words them.
const (
	closeReasonPaneGone      = "pane gone"
	closeReasonSessionEnded  = "session ended"
	closeReasonCallerGone    = "caller session gone"
	closeReasonDaemonRestart = "daemon restarted"
)

func closeNoticeKey(id string) string { return id + closeNoticeSuffix }

func isCloseNoticeKey(rec Record, key string) bool { return key == closeNoticeKey(rec.ID) }

func closeReasonIdle() string { return "idle " + shortDuration(idleCloseAfter) }

// shortDuration is d as whole hours or minutes when it is one, else as
// time.Duration prints it.
func shortDuration(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}

func lastTurnID(rec Record) string {
	if len(rec.Turns) == 0 {
		return ""
	}
	return rec.Turns[len(rec.Turns)-1].TurnID
}

// closeNoticeHeader is the line a close notice opens with: why the run
// closed, the turn whose result follows, and that the run takes no more.
func closeNoticeHeader(rec Record, reason string) string {
	name := sanitizeNotification(rec.Name)
	if name == "" {
		name = sanitizeNotification(rec.Template)
	}
	header := fmt.Sprintf("[leo] dispatch %s (%s) closed: %s · active %s", sanitizeNotification(rec.ID), name, sanitizeNotification(reason), formatActiveSeconds(rec.ActiveSeconds))
	if last := lastTurnID(rec); last != "" {
		header += " · last turn " + sanitizeNotification(last)
	}
	return header + " · " + closeNoticeTail
}

const closeNoticeTail = "run is closed; follow-ups will be rejected"

// addCloseNotice records rec's close notice, once: a run the caller never
// heard end (idle close, a dead pane or session, a daemon restart) says why it
// is gone, whatever its turns' own notices already delivered. covered is a
// wait on the run, which reports the close itself.
func (d *Dispatcher) addCloseNotice(rec *Record, reason string, covered bool) bool {
	key := closeNoticeKey(rec.ID)
	if _, exists := rec.Notifications[key]; exists {
		return false
	}
	if rec.Notifications == nil {
		rec.Notifications = make(map[string]Notification)
	}
	now := d.now()
	snapshot := cloneRecord(*rec)
	snapshot.ActiveSeconds = snapshot.LiveActiveSeconds(now)
	snapshot.RunningSince = nil
	n := Notification{Message: closeNoticeHeader(snapshot, reason) + pointerSuffix}
	if !rec.Notify || rec.CallerPaneID == "" || rec.CallerHarness == "" || covered {
		n.Disposition, n.SuppressedAt = NotificationSuppressed, now
	} else {
		n.Disposition, n.PendingAt = NotificationPending, now
	}
	rec.Notifications[key] = n
	return true
}

// waitCoversRunLocked reports a leo_wait on rec: on the run id, or on one of
// its turns (Wait([id]) registers the latest orchestrator turn's id).
func (d *Dispatcher) waitCoversRunLocked(rec Record) bool {
	if d.waits[rec.ID] > 0 {
		return true
	}
	for _, t := range rec.Turns {
		if d.waits[t.TurnID] > 0 {
			return true
		}
	}
	return false
}

// closeNoticeReady reports whether item may be delivered now. A close notice
// waits until every turn notice of its run is resolved, so the caller reads
// the last result before the close, and is dropped if a wait on the run came
// in since it was recorded.
func (d *Dispatcher) closeNoticeReady(item pendingNotification) bool {
	if !isCloseNoticeKey(item.record, item.key) {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rec := item.state.record
	n := rec.Notifications[item.key]
	if n.Disposition != NotificationPending {
		return true
	}
	if d.waitCoversRunLocked(rec) {
		n.Disposition, n.SuppressedAt = NotificationSuppressed, d.now()
		item.state.record.Notifications[item.key] = n
		_ = d.persistNotificationRecordLocked(item.state)
		return false
	}
	return !hasUnresolvedTurnNotice(rec)
}

func hasUnresolvedTurnNotice(rec Record) bool {
	for key, n := range rec.Notifications {
		if !isCloseNoticeKey(rec, key) && (n.Disposition == NotificationPending || n.Disposition == NotificationClaimed) {
			return true
		}
	}
	return false
}

func (d *Dispatcher) closeNoticeLocked(s *runState, reason string) {
	if !d.addCloseNotice(&s.record, reason, d.waitCoversRunLocked(s.record)) {
		return
	}
	if err := d.persistNotificationRecordLocked(s); err != nil {
		key := closeNoticeKey(s.record.ID)
		if n := s.record.Notifications[key]; n.Disposition == NotificationPending {
			n.Disposition, n.FailedAt = NotificationFailed, d.now()
			s.record.Notifications[key] = n
		}
		fmt.Fprintf(os.Stderr, "dispatch %s: recording close notice: %v\n", s.record.ID, err)
	}
}

// closeNoticeMessage is the close notice for transports that take the last
// turn's result inline: header, then that result delimited as subagent output.
func closeNoticeMessage(rec Record, pointer string) string {
	return inlineNotification(rec, lastTurnID(rec), strings.TrimSuffix(pointer, pointerSuffix))
}

// beginCloseLocked starts settling s into closed for reason, which its close
// notice words once the run ends.
func (d *Dispatcher) beginCloseLocked(s *runState, reason string, grace time.Duration) {
	if s.record.Status.Terminal() || s.record.Status == StatusSettling {
		return
	}
	s.closeReason = reason
	d.beginSettlementLocked(s, StatusClosed, grace)
}
