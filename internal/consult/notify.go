package consult

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
)

var (
	ErrNotificationNotSent    = errors.New("notification was not sent")
	ErrNotificationAmbiguous  = errors.New("notification delivery is ambiguous")
	ErrNotificationNotDurable = errors.New("notification record is not durable")
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

func (d *Dispatcher) completionCandidateLocked(s *runState, key string, status Status) bool {
	if key == "" {
		return true
	}
	if s.record.Notifications == nil {
		s.record.Notifications = make(map[string]Notification)
	}
	if _, exists := s.record.Notifications[key]; exists {
		return true
	}
	now := d.now()
	snapshot := cloneRecord(s.record)
	snapshot.ActiveSeconds = snapshot.LiveActiveSeconds(now)
	snapshot.RunningSince = nil
	n := Notification{Message: completionNotification(snapshot, key, status)}
	if !s.record.Notify || s.record.CallerPaneID == "" || s.record.CallerHarness == "" || d.waits[key] > 0 {
		n.Disposition, n.SuppressedAt = NotificationSuppressed, now
		if s.record.Notify && s.record.CallerPaneID != "" && s.record.CallerHarness == "" {
			fmt.Fprintf(os.Stderr, "dispatch %s: suppressing notification %s: unknown caller harness\n", s.record.ID, key)
		}
	} else {
		n.Disposition, n.PendingAt = NotificationPending, now
	}
	s.record.Notifications[key] = n
	if err := d.persistNotificationRecordLocked(s); err != nil {
		if n.Disposition == NotificationPending {
			n.Disposition, n.FailedAt = NotificationFailed, now
			s.record.Notifications[key] = n
		}
		fmt.Fprintf(os.Stderr, "dispatch %s: recording notification: %v\n", s.record.ID, err)
		return false
	}
	return true
}

func (d *Dispatcher) persistNotificationRecordLocked(s *runState) error {
	if h, ok := s.handle.(recordHandle); ok {
		return h.SetRecord(cloneRecord(s.record))
	}
	return ErrNotificationNotDurable
}

func (d *Dispatcher) restorePendingNotifications(rec Record) {
	for _, n := range rec.Notifications {
		if n.Disposition == NotificationPending || n.Disposition == NotificationClaimed {
			d.mu.Lock()
			if d.runs[rec.ID] == nil {
				var handle Handle = nopHandle{}
				if recorder, ok := d.recorder.(*FileRecorder); ok {
					handle = &restoredNotificationHandle{dir: recorder.dir, rec: rec}
				}
				done := make(chan struct{})
				if rec.Status.Terminal() {
					close(done)
				}
				d.runs[rec.ID] = &runState{record: rec, handle: handle, done: done}
			}
			d.mu.Unlock()
			return
		}
	}
}

type restoredNotificationHandle struct {
	mu  sync.Mutex
	dir string
	rec Record
}

func (h *restoredNotificationHandle) Write(p []byte) (int, error) { return len(p), nil }
func (h *restoredNotificationHandle) SetRecord(rec Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rec = rec
	return writeRecord(h.dir, rec)
}
func (h *restoredNotificationHandle) SetStatus(s Status) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rec.Status = s
	return writeRecord(h.dir, h.rec)
}
func (h *restoredNotificationHandle) SetText(text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.rec.Text = text
	return writeRecord(h.dir, h.rec)
}
func (h *restoredNotificationHandle) SetViewerWindowID(id string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
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
		n := Notification{Message: completionNotification(*rec, key, turnNotificationStatus(*rec, key))}
		if !rec.Notify || rec.CallerPaneID == "" || rec.CallerHarness == "" || d.waits[key] > 0 {
			n.Disposition, n.SuppressedAt = NotificationSuppressed, d.now()
		} else {
			n.Disposition, n.PendingAt = NotificationPending, d.now()
		}
		rec.Notifications[key] = n
	}
	for _, turn := range rec.Turns {
		if turn.Outcome != "" {
			add(turn.TurnID)
		}
	}
}

type pendingNotification struct {
	state  *runState
	record Record
	key    string
}

func recordHasUnresolvedNotification(rec Record) bool {
	for _, n := range rec.Notifications {
		if n.Disposition == NotificationPending || n.Disposition == NotificationClaimed {
			return true
		}
	}
	return false
}

// SweepNotifications claims and delivers pending notifications. All external
// I/O occurs without Dispatcher.mu; claims are durable before submission.
func (d *Dispatcher) SweepNotifications(ctx context.Context) {
	d.mu.Lock()
	delivery := d.notificationDelivery
	var pending []pendingNotification
	for _, s := range d.runs {
		for key, n := range s.record.Notifications {
			if n.Disposition == NotificationPending || n.Disposition == NotificationClaimed {
				transitionAt := n.PendingAt
				if n.Disposition == NotificationClaimed {
					transitionAt = n.ClaimedAt
				}
				if !transitionAt.IsZero() && !d.now().Before(transitionAt.Add(time.Hour)) {
					n.Disposition, n.FailedAt = NotificationFailed, d.now()
					s.record.Notifications[key] = n
					if err := d.persistNotificationRecordLocked(s); err != nil {
						fmt.Fprintf(os.Stderr, "dispatch %s: notification %s expired but persistence failed: %v\n", s.record.ID, key, err)
					} else {
						fmt.Fprintf(os.Stderr, "dispatch %s: notification %s expired after 1h\n", s.record.ID, key)
					}
					continue
				}
				if delivery != nil {
					pending = append(pending, pendingNotification{s, cloneRecord(s.record), key})
				}
			}
		}
	}
	d.mu.Unlock()
	for _, item := range pending {
		unlock := d.serialLocks([]string{item.record.CallerPaneID})
		itemCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		d.deliverNotification(itemCtx, delivery, item)
		cancel()
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
		_ = d.persistNotificationRecordLocked(item.state)
		d.mu.Unlock()
		return
	}
	n.Disposition, n.ClaimedAt = NotificationClaimed, d.now()
	item.state.record.Notifications[item.key] = n
	if err := d.persistNotificationRecordLocked(item.state); err != nil {
		n.Disposition, n.FailedAt = NotificationFailed, d.now()
		item.state.record.Notifications[item.key] = n
		_ = d.persistNotificationRecordLocked(item.state)
		fmt.Fprintf(os.Stderr, "dispatch %s: notification %s not delivered: durable claim failed: %v\n", item.state.record.ID, item.key, err)
		d.mu.Unlock()
		return
	}
	rec := cloneRecord(item.state.record)
	d.mu.Unlock()
	message := n.Message
	if message == "" {
		message = completionNotification(rec, item.key, turnNotificationStatus(rec, item.key))
	}
	err := delivery.Deliver(ctx, rec, message)
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
	_ = d.persistNotificationRecordLocked(item.state)
	d.mu.Unlock()
}
