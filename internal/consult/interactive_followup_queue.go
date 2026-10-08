package consult

import (
	"fmt"
	"os"

	"github.com/blackpaw-studio/leo/internal/config"
)

// queuedSend is a follow-up turn waiting in the slot line. cancel closes when
// the turn resolves before its slot comes (cancel, close, steer), so the
// waiting goroutine leaves the line instead of hanging.
type queuedSend struct {
	turnID string
	waiter *slotWaiter
	cancel chan struct{}
}

// ApplyConfig sets the dispatch slot cap from cfg (nil leaves it as is). A
// raised cap admits queued turns immediately; a lowered one only holds back
// new admissions.
func (d *Dispatcher) ApplyConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	d.slots.SetMax(cfg.DispatchMaxConcurrent())
}

func queuedOvertakenText(id string) string {
	return fmt.Sprintf("dispatch %s was steered (a human typed into its pane) before a concurrency slot freed, so this queued follow-up was not sent.", id)
}

// queueSendLocked records a follow-up that found every slot busy. The turn
// holds its place in the slot line (waiter) and is sent when it comes up; it
// never touches the pane before then.
func (d *Dispatcher) queueSendLocked(s *runState, message string, waiter *slotWaiter) SendResult {
	d.loseUndeliveredLocked(s)
	wantPane := s.paneWant
	t := d.buildTurnLocked(s, TurnSourceOrchestrator, message, false)
	s.paneWant = wantPane
	t.Queued = true
	q := &queuedSend{turnID: t.TurnID, waiter: waiter, cancel: make(chan struct{})}
	s.queuedSend = q
	d.persistLocked(s, "turn")
	go d.runQueuedSend(s, q, message, waiter)
	return SendResult{TurnID: t.TurnID, Queued: true}
}

// dequeueSendLocked wakes the waiting goroutine of queued turn turnID when
// the turn resolved without ever starting.
func (d *Dispatcher) dequeueSendLocked(s *runState, turnID string) {
	if q := s.queuedSend; q != nil && q.turnID == turnID {
		s.queuedSend = nil
		d.slots.Cancel(q.waiter)
		close(q.cancel)
	}
}

// runQueuedSend waits for the turn's place in line, then starts it exactly as
// an immediate follow-up would. A turn resolved while waiting never sends.
func (d *Dispatcher) runQueuedSend(s *runState, q *queuedSend, message string, waiter *slotWaiter) {
	select {
	case <-waiter.Ready():
	case <-q.cancel:
		return
	}
	d.mu.Lock()
	if s.record.Status.Terminal() || s.record.Status == StatusSettling || turnByID(s.record, q.turnID).Outcome != "" {
		d.mu.Unlock()
		d.slots.Cancel(waiter)
		return
	}
	// Admission: the slot now belongs to the turn, released once by whatever
	// closes it.
	s.queuedSend = nil
	for i := range s.record.Turns {
		if s.record.Turns[i].TurnID == q.turnID {
			s.record.Turns[i].Queued, s.record.Turns[i].SlotHeld = false, true
			s.record.Turns[i].StartedAt = d.now()
		}
	}
	s.paneWant = "split"
	d.persistLocked(s, "turn")
	d.mu.Unlock()
	if _, err := d.deliverSend(d.daemonCtx, s, q.turnID, message); err != nil {
		fmt.Fprintf(os.Stderr, "dispatch %s: sending queued follow-up %s: %v\n", s.record.ID, q.turnID, err)
	}
}
