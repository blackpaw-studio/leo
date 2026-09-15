package consult

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

var (
	ErrNotificationNotSent   = errors.New("notification was not sent")
	ErrNotificationAmbiguous = errors.New("notification delivery is ambiguous")
)

// NotificationDelivery separates passive readiness checks from submission.
// Ready must not send keys or otherwise mutate the caller.
type NotificationDelivery interface {
	Ready(context.Context, Record) bool
	Deliver(context.Context, Record, string) error
}

func (d *Dispatcher) SetNotificationDelivery(delivery NotificationDelivery) {
	d.mu.Lock()
	d.notificationDelivery = delivery
	d.mu.Unlock()
}

func sanitizeNotification(value string) string {
	clean := strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, value))
	return strings.Join(strings.Fields(clean), " ")
}

func completionNotification(rec Record, key string, status Status) string {
	if key == "" {
		key = rec.ID
	}
	name := sanitizeNotification(rec.Name)
	if name == "" {
		name = sanitizeNotification(rec.Template)
	}
	return fmt.Sprintf("[leo] dispatch %s (%s) %s · active %s — collect with leo_wait", sanitizeNotification(key), name, status, formatActiveSeconds(rec.ActiveSeconds))
}

func turnNotificationStatus(rec Record, key string) Status {
	for _, turn := range rec.Turns {
		if turn.TurnID != key {
			continue
		}
		switch turn.Outcome {
		case TurnFinished:
			return StatusDone
		case TurnInterrupted:
			if rec.Status == StatusTimeout {
				return StatusTimeout
			}
			return StatusCanceled
		case TurnRejected, TurnLost:
			return StatusFailed
		}
	}
	return rec.Status
}

func (d *Dispatcher) completionCandidateLocked(s *runState, key string, status Status) {
	if key == "" {
		return
	}
	if s.record.Notifications == nil {
		s.record.Notifications = make(map[string]Notification)
	}
	if _, exists := s.record.Notifications[key]; exists {
		return
	}
	now := d.now()
	n := Notification{}
	if !s.record.Notify || s.record.CallerPaneID == "" || d.waits[key] > 0 {
		n.Disposition, n.SuppressedAt = NotificationSuppressed, now
	} else {
		n.Disposition, n.PendingAt = NotificationPending, now
	}
	s.record.Notifications[key] = n
	d.persistRecordLocked(s)
	_ = status // status is reconstructed durably from the record at delivery.
}

func (d *Dispatcher) restorePendingNotifications(rec Record) {
	for _, n := range rec.Notifications {
		if n.Disposition == NotificationPending {
			d.mu.Lock()
			if d.runs[rec.ID] == nil {
				var handle Handle = nopHandle{}
				if recorder, ok := d.recorder.(*FileRecorder); ok {
					handle = &restoredNotificationHandle{dir: recorder.dir, rec: rec}
				}
				d.runs[rec.ID] = &runState{record: rec, handle: handle, done: make(chan struct{})}
			}
			d.mu.Unlock()
			return
		}
	}
}

type restoredNotificationHandle struct {
	dir string
	rec Record
}

func (h *restoredNotificationHandle) Write(p []byte) (int, error) { return len(p), nil }
func (h *restoredNotificationHandle) SetRecord(rec Record) error {
	h.rec = rec
	return writeRecord(h.dir, rec)
}
func (h *restoredNotificationHandle) SetStatus(s Status) error {
	h.rec.Status = s
	return writeRecord(h.dir, h.rec)
}
func (h *restoredNotificationHandle) SetText(text string) error {
	h.rec.Text = text
	return writeRecord(h.dir, h.rec)
}
func (h *restoredNotificationHandle) SetViewerWindowID(id string) error {
	h.rec.ViewerWindowID = id
	return writeRecord(h.dir, h.rec)
}
func (h *restoredNotificationHandle) Close(Status, error) error { return nil }

func (d *Dispatcher) addRestartCandidates(rec *Record) {
	if rec.Notifications == nil {
		rec.Notifications = make(map[string]Notification)
	}
	add := func(key string) {
		if _, ok := rec.Notifications[key]; ok {
			return
		}
		n := Notification{}
		if !rec.Notify || rec.CallerPaneID == "" || d.waits[key] > 0 {
			n.Disposition, n.SuppressedAt = NotificationSuppressed, d.now()
		} else {
			n.Disposition, n.PendingAt = NotificationPending, d.now()
		}
		rec.Notifications[key] = n
	}
	if rec.Mode != ModeInteractive {
		add(rec.ID)
	} else {
		for _, turn := range rec.Turns {
			if turn.Outcome != "" {
				add(turn.TurnID)
			}
		}
	}
}

type pendingNotification struct {
	state  *runState
	record Record
	key    string
}

// SweepNotifications claims and delivers pending notifications. All external
// I/O occurs without Dispatcher.mu; claims are durable before submission.
func (d *Dispatcher) SweepNotifications(ctx context.Context) {
	d.mu.Lock()
	delivery := d.notificationDelivery
	var pending []pendingNotification
	if delivery != nil {
		for _, s := range d.runs {
			for key, n := range s.record.Notifications {
				if n.Disposition == NotificationPending {
					pending = append(pending, pendingNotification{s, cloneRecord(s.record), key})
				}
			}
		}
	}
	d.mu.Unlock()
	for _, item := range pending {
		unlock := d.serialLocks([]string{item.record.CallerPaneID})
		d.deliverNotification(ctx, delivery, item)
		unlock()
	}
}

func (d *Dispatcher) deliverNotification(ctx context.Context, delivery NotificationDelivery, item pendingNotification) {
	if !delivery.Ready(ctx, item.record) {
		return
	}
	d.mu.Lock()
	n := item.state.record.Notifications[item.key]
	if n.Disposition != NotificationPending {
		d.mu.Unlock()
		return
	}
	if d.waits[item.key] > 0 {
		n.Disposition, n.SuppressedAt = NotificationSuppressed, d.now()
		item.state.record.Notifications[item.key] = n
		d.persistRecordLocked(item.state)
		d.mu.Unlock()
		return
	}
	n.Disposition, n.ClaimedAt = NotificationClaimed, d.now()
	item.state.record.Notifications[item.key] = n
	d.persistRecordLocked(item.state)
	rec := cloneRecord(item.state.record)
	d.mu.Unlock()
	err := delivery.Deliver(ctx, rec, completionNotification(rec, item.key, turnNotificationStatus(rec, item.key)))
	d.mu.Lock()
	n = item.state.record.Notifications[item.key]
	switch {
	case err == nil:
		n.Disposition, n.DeliveredAt = NotificationDelivered, d.now()
	case errors.Is(err, ErrNotificationNotSent):
		n.Disposition, n.ClaimedAt = NotificationPending, time.Time{}
	default:
		n.Disposition, n.FailedAt = NotificationFailed, d.now()
	}
	item.state.record.Notifications[item.key] = n
	d.persistRecordLocked(item.state)
	d.mu.Unlock()
}
