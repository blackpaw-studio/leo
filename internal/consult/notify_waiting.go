package consult

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// waitingNoticeAfter is how long a run may wait on background work with no
// hook activity before its caller hears about it.
const waitingNoticeAfter = 30 * time.Minute

// waitingNoticeInfix joins a run id and the start of the quiet stretch a
// waiting notice is for: <id>#waiting@<unix seconds>.
const waitingNoticeInfix = "#waiting@"

func waitingNoticeKey(id string, episode time.Time) string {
	return id + waitingNoticeInfix + strconv.FormatInt(episode.Unix(), 10)
}

func isWaitingNoticeKey(rec Record, key string) bool {
	return strings.HasPrefix(key, rec.ID+waitingNoticeInfix)
}

// isRunNoticeKey reports a notice about the run rather than one of its
// turns: its close notice or a waiting notice. Such a notice carries no turn
// result, so delivering it never collects or releases the run.
func isRunNoticeKey(rec Record, key string) bool {
	return isCloseNoticeKey(rec, key) || isWaitingNoticeKey(rec, key)
}

// waitingEpisodeLocked is when s's current quiet stretch of waiting began:
// its last hook activity or its entry into waiting, whichever is later. Any
// hook or a fresh entry into waiting starts a new stretch, so each stretch
// gets its own notice key.
func waitingEpisodeLocked(s *runState) time.Time {
	if s.waitingSince.After(s.record.HookActivity) {
		return s.waitingSince
	}
	return s.record.HookActivity
}

// waitingNoticeLocked records s's waiting notice once its current quiet
// stretch reaches waitingNoticeAfter. It changes nothing else: the run keeps
// waiting, and its stalled flag keeps its own threshold.
func (d *Dispatcher) waitingNoticeLocked(s *runState, now time.Time) {
	if s.record.Status != StatusWaiting {
		return
	}
	episode := waitingEpisodeLocked(s)
	if episode.IsZero() || now.Sub(episode) < waitingNoticeAfter {
		return
	}
	key := waitingNoticeKey(s.record.ID, episode)
	if _, exists := s.record.Notifications[key]; exists {
		return
	}
	if s.record.Notifications == nil {
		s.record.Notifications = make(map[string]Notification)
	}
	n := Notification{Message: waitingNoticeMessage(s.record, now.Sub(episode)) + pointerSuffix}
	if !s.record.Notify || s.record.CallerPaneID == "" || s.record.CallerHarness == "" || d.waitCoversRunLocked(s.record) {
		n.Disposition, n.SuppressedAt = NotificationSuppressed, now
	} else {
		n.Disposition, n.PendingAt = NotificationPending, now
	}
	s.record.Notifications[key] = n
	if err := d.persistNotificationRecordLocked(s); err != nil {
		if n.Disposition == NotificationPending {
			n.Disposition, n.FailedAt = NotificationFailed, now
			s.record.Notifications[key] = n
		}
		fmt.Fprintf(os.Stderr, "dispatch %s: recording waiting notice: %v\n", s.record.ID, err)
	}
}

// waitingNoticeMessage tells the caller a run has sat waiting, quiet for
// quiet, and what it can do about it.
func waitingNoticeMessage(rec Record, quiet time.Duration) string {
	name := sanitizeNotification(rec.Name)
	if name == "" {
		name = sanitizeNotification(rec.Template)
	}
	return fmt.Sprintf("[leo] dispatch %s (%s) waiting on %s · quiet %s · leo_release or leo_cancel it, or keep waiting",
		sanitizeNotification(rec.ID), name, sanitizeNotification(rec.PendingWork.Summary()), shortDuration(quiet.Truncate(time.Minute)))
}

// waitingNoticeStale reports a waiting notice whose quiet stretch is over by
// the time it would be sent: the run moved on, so the notice would mislead.
func waitingNoticeStale(s *runState, key string) bool {
	return s.record.Status != StatusWaiting || key != waitingNoticeKey(s.record.ID, waitingEpisodeLocked(s))
}
