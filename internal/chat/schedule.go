package chat

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	// maxScheduleHorizon bounds how far ahead a reminder may be set, in years.
	maxScheduleHorizon = 1
	// lateFireThreshold is how overdue a reminder may be and still post
	// without the late line (a daemon that was down past it adds one).
	lateFireThreshold = 60 * time.Second
	// scheduleRetry is how long the loop waits to retry after a store error.
	scheduleRetry = time.Minute
	// lateTimeLayout renders when a late reminder was meant to fire.
	lateTimeLayout = "Mon 2 Jan 2006 15:04 MST"
)

// ScheduleRequest is leo_chat_schedule. Caller, Task and RunID are as for
// PostRequest; At is an RFC3339 time with an offset.
type ScheduleRequest struct {
	Caller string
	Task   string
	RunID  string
	Text   string
	At     string
}

// Schedule stores a reminder to post Text into the caller's thread at At.
// The thread is resolved now, so a later config change does not move it.
func (s *Service) Schedule(_ context.Context, req ScheduleRequest) (Schedule, error) {
	if err := s.enter(); err != nil {
		return Schedule{}, err
	}
	defer s.leave()
	text := strings.TrimSpace(req.Text)
	switch {
	case text == "":
		return Schedule{}, fmt.Errorf("%w: a reminder needs text", ErrInvalid)
	case len(req.Text) > MaxTextBytes:
		return Schedule{}, fmt.Errorf("%w: text exceeds %d bytes", ErrInvalid, MaxTextBytes)
	}
	at, err := time.Parse(time.RFC3339, req.At)
	if err != nil {
		return Schedule{}, fmt.Errorf("%w: at must be an RFC3339 time with an offset, like 2026-10-14T09:00:00-04:00", ErrInvalid)
	}
	now := s.opts.Now()
	switch {
	case !at.After(now):
		return Schedule{}, fmt.Errorf("%w: at must be in the future", ErrInvalid)
	case at.After(now.AddDate(maxScheduleHorizon, 0, 0)):
		return Schedule{}, fmt.Errorf("%w: at must be within one year", ErrInvalid)
	}
	req.Task, req.RunID = s.withRun(req.Caller, req.Task, req.RunID)
	agent, _, err := s.thread(req.Caller, req.Task)
	if err != nil {
		return Schedule{}, err
	}
	r := Schedule{ID: s.opts.IDs.New(), Agent: agent, Kind: KindText, Text: text, FireAt: at, Created: now}
	if req.Task != "" {
		r.Kind, r.Task, r.RunID = KindTask, req.Task, req.RunID
	}
	if err := s.opts.Store.InsertSchedule(r); err != nil {
		return Schedule{}, err
	}
	s.kick()
	return r, nil
}

// kick tells the reminder loop its pending set changed.
func (s *Service) kick() {
	select {
	case s.rearm <- struct{}{}:
	default:
	}
}

// ScheduleListRequest is leo_chat_schedule_list.
type ScheduleListRequest struct {
	Caller string
	Task   string
	RunID  string
}

// ScheduleCancelRequest is leo_chat_schedule_cancel.
type ScheduleCancelRequest struct {
	Caller string
	Task   string
	RunID  string
	ID     string
}

// Schedules lists the pending reminders of the caller's thread, soonest first.
func (s *Service) Schedules(_ context.Context, req ScheduleListRequest) ([]Schedule, error) {
	if err := s.enter(); err != nil {
		return nil, err
	}
	defer s.leave()
	task, _ := s.withRun(req.Caller, req.Task, req.RunID)
	agent, _, err := s.thread(req.Caller, task)
	if err != nil {
		return nil, err
	}
	return s.opts.Store.PendingSchedules(agent)
}

// CancelSchedule cancels a pending reminder of the caller's thread: ErrNotFound
// when there is no such reminder there, ErrConflict when it has already fired
// or been cancelled.
func (s *Service) CancelSchedule(_ context.Context, req ScheduleCancelRequest) error {
	if err := s.enter(); err != nil {
		return err
	}
	defer s.leave()
	task, _ := s.withRun(req.Caller, req.Task, req.RunID)
	agent, _, err := s.thread(req.Caller, task)
	if err != nil {
		return err
	}
	if !validID(req.ID) {
		return ErrNotFound
	}
	if err := s.opts.Store.CancelSchedule(req.ID, agent, s.opts.Now()); err != nil {
		return err
	}
	s.kick()
	return nil
}

// runSchedules is the reminder loop: fire what is due, arm a timer for the
// earliest pending reminder, and wait for that timer, a kick (a reminder
// was added or cancelled) or shutdown. Which timer woke it never decides
// anything — firing is guarded in SQL — so a stale timer is harmless.
func (s *Service) runSchedules() {
	var timer <-chan time.Time
	var armedID string
	var armedAt time.Time
	for {
		fireErr := s.fireDue()
		next, ok, err := s.opts.Store.NextPending()
		switch {
		case err != nil || fireErr != nil:
			// A failed read or fire leaves overdue reminders pending: wait the
			// retry interval (a kick still wakes the loop) rather than spin.
			if err != nil {
				s.opts.Logf("chat: reading the next reminder: %v", err)
			}
			timer, armedID = s.opts.After(scheduleRetry), ""
		case !ok:
			timer, armedID = nil, ""
		case next.ID != armedID || !next.FireAt.Equal(armedAt):
			// A kick that left the earliest reminder alone keeps its timer.
			timer, armedID, armedAt = s.opts.After(max(next.FireAt.Sub(s.opts.Now()), 0)), next.ID, next.FireAt
		}
		select {
		case <-s.ctx.Done():
			return
		case <-s.rearm:
		case <-timer:
			timer, armedID = nil, ""
		}
	}
}

// fireDue posts every reminder that is due now, returning the first error
// (already logged) of a read or fire that failed: those reminders stay pending.
func (s *Service) fireDue() error {
	now := s.opts.Now()
	due, err := s.opts.Store.DuePending(now)
	if err != nil {
		s.opts.Logf("chat: reading due reminders: %v", err)
		return err
	}
	var first error
	for _, r := range due {
		if s.ctx.Err() != nil {
			return first
		}
		if err := s.fire(r, now); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// fire posts reminder r. The reminder is marked fired in the same
// transaction as its message, so it posts at most once; a reminder that is
// no longer pending (cancelled meanwhile) posts nothing.
func (s *Service) fire(r Schedule, now time.Time) error {
	text := r.Text
	if now.Sub(r.FireAt) > lateFireThreshold {
		text += "\n\n_(late — scheduled for " + r.FireAt.Local().Format(lateTimeLayout) + ")_"
	}
	var fired bool
	m, err := s.insertWith(Message{Agent: r.Agent, Direction: DirectionOut, Kind: r.Kind, Task: r.Task, RunID: r.RunID, Text: text}, func(m Message) (err error) {
		fired, err = s.opts.Store.FireSchedule(r.ID, m, now)
		return err
	})
	if err != nil {
		s.opts.Logf("chat: firing reminder %s: %v", r.ID, err)
		return err
	}
	if !fired {
		return nil
	}
	// The reminder has fired; failing to announce it is not a fire failure.
	stored, err := s.opts.Store.Message(m.ID)
	if err != nil {
		s.opts.Logf("chat: reading fired reminder %s: %v", r.ID, err)
		return nil
	}
	s.publish(EventMessage, messageEvent(stored))
	return nil
}
