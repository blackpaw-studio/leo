package service

import "github.com/blackpaw-studio/leo/internal/observe"

// SetPublisher wires an observe.Publisher into the supervisor so agent
// lifecycle transitions are announced on the event bus. Optional: an unset
// (nil) publisher makes every publish call a safe no-op, so existing
// NewSupervisor callers are unaffected.
func (s *Supervisor) SetPublisher(p observe.Publisher) {
	s.mu.Lock()
	s.publisher = p
	s.mu.Unlock()
}

// publish is a nil-safe no-op when no publisher has been configured.
func (s *Supervisor) publish(ev observe.Event) {
	s.mu.RLock()
	p := s.publisher
	s.mu.RUnlock()
	if p == nil {
		return
	}
	p.Publish(ev)
}

// toObserveStatus maps the supervisor's internal status vocabulary onto the
// wire contract's lifecycle enum. "restarting" — a purely internal
// crash-loop-backoff state — folds into "starting", the closest lifecycle
// equivalent a consumer can act on.
func toObserveStatus(status string) observe.Status {
	switch status {
	case "running":
		return observe.StatusRunning
	case "starting", "restarting":
		return observe.StatusStarting
	case "stopped":
		return observe.StatusStopped
	default:
		return observe.Status(status)
	}
}
