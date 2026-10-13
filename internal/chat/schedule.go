package chat

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// maxScheduleHorizon bounds how far ahead a reminder may be set.
const maxScheduleHorizon = 1 // years

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
