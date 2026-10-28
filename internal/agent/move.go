package agent

import (
	"errors"
	"fmt"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
)

// MoveMode is the kind of transfer holding an agent's move lock.
type MoveMode string

const (
	// MoveModeMove hands the agent to another host: while it is held,
	// nothing may launch the agent here.
	MoveModeMove MoveMode = "move"
	// MoveModeCopy forks the agent: the source keeps running, so the lock
	// only keeps a second move or copy out.
	MoveModeCopy MoveMode = "copy"
)

var (
	// ErrAgentMoved is matched (errors.Is) by every *MovedError.
	ErrAgentMoved = errors.New("agent moved to another host")
	// ErrAgentMoving is returned while a move holds the agent's lock: a launch
	// then could leave two live copies of one conversation.
	ErrAgentMoving = errors.New("agent is being moved")
	// ErrMovePending is matched (errors.Is) by every *MovePendingError.
	ErrMovePending = errors.New("agent has a move pending")
)

// MovePendingError refuses a verb on an agent fenced by a move whose
// outcome is unknown: it may be live on the destination.
type MovePendingError struct {
	Name    string
	Pending agentstore.MovePending
}

func (e *MovePendingError) Error() string {
	p := e.Pending
	return fmt.Sprintf("agent %s has a move to %s pending since %s (run %s) whose outcome is unknown, so it may be live there: check `leo --host %s agent list`, then settle it with `leo agent move %s --reconcile` (or `leo agent move %s --abandon-remote` if %s is gone for good)",
		e.Name, p.To, p.Since.Format(time.RFC3339), p.RunID, p.To, e.Name, e.Name, p.To)
}

// Is makes errors.Is(err, ErrMovePending) match.
func (e *MovePendingError) Is(target error) bool { return target == ErrMovePending }

// pendingError returns the refusal for rec, or nil when no move is pending.
func pendingError(rec agentstore.Record) error {
	if rec.MovePending == nil {
		return nil
	}
	return &MovePendingError{Name: rec.Name, Pending: *rec.MovePending}
}

// MovedError refuses a lifecycle verb on an agent whose live home is now
// another host, and says where to find it.
type MovedError struct {
	Name string
	To   string
	At   time.Time
}

func (e *MovedError) Error() string {
	when := ""
	if !e.At.IsZero() {
		when = " on " + e.At.Format(time.RFC3339)
	}
	return fmt.Sprintf("agent %s moved to %s%s; use `leo --host %s agent attach %s`, or move it back with `leo --host %s agent move %s --to <this host>`",
		e.Name, e.To, when, e.To, e.Name, e.To, e.Name)
}

// Is makes errors.Is(err, ErrAgentMoved) match.
func (e *MovedError) Is(target error) bool { return target == ErrAgentMoved }

// movedError returns the refusal for rec, or nil when it is not moved.
func movedError(rec agentstore.Record) error {
	if !rec.Moved() {
		return nil
	}
	e := &MovedError{Name: rec.Name, To: rec.MovedTo}
	if rec.MovedAt != nil {
		e.At = *rec.MovedAt
	}
	return e
}

// checkMovable refuses a verb that would launch, rewire or re-key rec: it is
// moved to another host, a move of it is pending, or one is in flight.
func (m *Manager) checkMovable(rec agentstore.Record) error {
	if err := movedError(rec); err != nil {
		return err
	}
	if err := pendingError(rec); err != nil {
		return err
	}
	if m.moveModeOf(rec.Name) == MoveModeMove {
		return fmt.Errorf("%w: %q", ErrAgentMoving, rec.Name)
	}
	return nil
}

// BeginMove takes name's move lock for one transfer. It fails with
// ErrAgentMoving when another move or copy holds it. release is idempotent.
// The lock lives in memory only: a daemon crash drops it, leaving a stopped,
// unmarked source that `leo agent start` recovers.
func (m *Manager) BeginMove(name string, mode MoveMode) (release func(), err error) {
	m.moveMu.Lock()
	defer m.moveMu.Unlock()
	if held, ok := m.moves[name]; ok {
		return nil, fmt.Errorf("%w: %q (a %s is in progress)", ErrAgentMoving, name, held)
	}
	if m.moves == nil {
		m.moves = map[string]MoveMode{}
	}
	m.moves[name] = mode
	released := false
	return func() {
		m.moveMu.Lock()
		defer m.moveMu.Unlock()
		if !released {
			delete(m.moves, name)
			released = true
		}
	}, nil
}

// moveModeOf is the mode holding name's move lock, "" when none does.
func (m *Manager) moveModeOf(name string) MoveMode {
	m.moveMu.Lock()
	defer m.moveMu.Unlock()
	return m.moves[name]
}

// Moved returns name's stored record when it is marked moved to another host.
func (m *Manager) Moved(name string) (agentstore.Record, bool) {
	rec, ok := m.stored(name)
	return rec, ok && rec.Moved()
}

// MovedErr returns the *MovedError for name, or its *MovePendingError while
// a move of it is pending, nil otherwise.
func (m *Manager) MovedErr(name string) error {
	rec, ok := m.stored(name)
	if !ok {
		return nil
	}
	if err := movedError(rec); err != nil {
		return err
	}
	return pendingError(rec)
}

// stored is name's agentstore record; load failures read as absent.
func (m *Manager) stored(name string) (agentstore.Record, bool) {
	cfg, err := m.cfgLoader()
	if err != nil {
		return agentstore.Record{}, false
	}
	stored, err := agentstore.Load(agentstore.FilePath(cfg.HomePath))
	if err != nil {
		return agentstore.Record{}, false
	}
	rec, ok := stored[name]
	return rec, ok
}

// FenceMove records on name that a move's commit is starting, before it
// starts: until the move is settled (MarkMoved, ClearMoveFence), nothing
// launches, wakes, moves or rewires the agent here, across daemon restarts.
func (m *Manager) FenceMove(name string, p agentstore.MovePending) error {
	cfg, err := m.cfgLoader()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	return agentstore.Update(cfg.HomePath, name, func(r agentstore.Record) agentstore.Record {
		r.MovePending = &p
		return r
	})
}

// ClearMoveFence lifts name's fence when it is run runID's; another run's
// fence, or none, is left as it is.
func (m *Manager) ClearMoveFence(name, runID string) error {
	cfg, err := m.cfgLoader()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	return agentstore.Update(cfg.HomePath, name, func(r agentstore.Record) agentstore.Record {
		if r.MovePending != nil && r.MovePending.RunID == runID {
			r.MovePending = nil
		}
		return r
	})
}

// MarkMoved marks name dormant here with its new home: to is the host key
// shown to operators, peer the other side's hostname for the move log. It
// settles a pending move, lifting its fence.
func (m *Manager) MarkMoved(name, to, peer string, at time.Time) error {
	cfg, err := m.cfgLoader()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	return agentstore.Update(cfg.HomePath, name, func(r agentstore.Record) agentstore.Record {
		r.Stopped = true
		r.WakeOnMessage = false
		r.MovedTo = to
		r.MovedAt = &at
		r.MoveLog = withMoveLog(r.MoveLog, peer, at)
		r.MovePending = nil
		return r
	})
}

// withMoveLog returns a copy of log with peer set to at.
func withMoveLog(log map[string]time.Time, peer string, at time.Time) map[string]time.Time {
	out := make(map[string]time.Time, len(log)+1)
	for k, v := range log {
		out[k] = v
	}
	if peer != "" {
		out[peer] = at
	}
	return out
}
