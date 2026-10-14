package drain

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Phase is where the manager is in a drain's life.
type Phase string

const (
	PhaseIdle      Phase = "idle"
	PhaseDraining  Phase = "draining"
	PhaseCommitted Phase = "committed"
)

// MemberState is one drain-set agent's progress.
type MemberState string

const (
	// StateWaiting: the pause notice has not reached the agent yet.
	StateWaiting MemberState = "waiting"
	// StateNotified: the agent has the pause notice and is not settled idle.
	StateNotified MemberState = "notified"
	// StateDrained: notified, then idle on SettlePolls consecutive polls.
	StateDrained MemberState = "drained"
	// StateGone: the agent no longer exists; counts as drained.
	StateGone MemberState = "gone"
)

var (
	ErrInProgress = errors.New("drain: a drain is already in progress")
	ErrNotDrained = errors.New("drain: agents are still working")
	ErrNoDrain    = errors.New("drain: no drain in progress")
)

const (
	defaultPollInterval = 2 * time.Second
	defaultLeaseTTL     = 60 * time.Second
	defaultSettlePolls  = 2
)

// MemberStatus is one drain-set agent as Status reports it.
type MemberStatus struct {
	Name      string      `json:"name"`
	Transport string      `json:"transport,omitempty"`
	State     MemberState `json:"state"`
}

// Status is a snapshot of the manager.
type Status struct {
	ID             string         `json:"id,omitempty"`
	Phase          Phase          `json:"phase"`
	Done           bool           `json:"done"`
	TimeoutMinutes int            `json:"timeout_minutes,omitempty"`
	Warning        string         `json:"warning,omitempty"`
	Members        []MemberStatus `json:"members,omitempty"`
}

// Deps are the manager's collaborators and tunables. Zero tunables take
// their defaults: PollInterval 2s, LeaseTTL 60s, SettlePolls 2.
type Deps struct {
	Prober   Prober
	Notifier Notifier
	Cron     CronGate
	Store    Store
	Clock    Clock
	Logf     func(format string, args ...any)

	PollInterval time.Duration
	// LeaseTTL cancels a drain nobody has asked about for this long: the
	// CLI driving it was killed or lost its connection.
	LeaseTTL    time.Duration
	SettlePolls int
}

type member struct {
	name      string
	transport string
	state     MemberState
	// pauseSent: the pause notice was delivered or is queued in the
	// agent's outbox, so a cancel must follow it.
	pauseSent bool
	// queued: the pause is in the outbox; the member is notified once the
	// outbox no longer holds it.
	queued    bool
	idleTicks int
}

func (m *member) settled() bool { return m.state == StateDrained || m.state == StateGone }

type activeDrain struct {
	id         string
	phase      Phase
	timeout    time.Duration
	members    []*member
	waitOnly   bool // no message path: wait for idle only
	warning    string
	lastStatus time.Time
}

// Manager runs at most one drain at a time. opMu serializes the operations
// that do I/O (Begin, Tick, Commit, Cancel); mu guards the state those
// operations and Status read, so Status never waits on a probe.
type Manager struct {
	deps Deps
	opMu sync.Mutex
	mu   sync.Mutex
	cur  *activeDrain
	wake chan struct{}
}

// New returns an idle manager.
func New(deps Deps) *Manager {
	if deps.PollInterval <= 0 {
		deps.PollInterval = defaultPollInterval
	}
	if deps.LeaseTTL <= 0 {
		deps.LeaseTTL = defaultLeaseTTL
	}
	if deps.SettlePolls <= 0 {
		deps.SettlePolls = defaultSettlePolls
	}
	if deps.Clock == nil {
		deps.Clock = SystemClock{}
	}
	if deps.Logf == nil {
		deps.Logf = func(string, ...any) {}
	}
	return &Manager{deps: deps, wake: make(chan struct{}, 1)}
}

// Begin starts a drain over the agents that are busy now, holds cron, and
// sends the pause notice to bridge members. An empty set is done at once.
func (m *Manager) Begin(ctx context.Context, timeout time.Duration) (Status, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if m.active() {
		return Status{}, ErrInProgress
	}
	busy, err := m.deps.Prober.BusyAgents(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("drain: listing busy agents: %w", err)
	}
	id, err := newDrainID()
	if err != nil {
		return Status{}, err
	}
	d := &activeDrain{id: id, phase: PhaseDraining, timeout: timeout, lastStatus: m.deps.Clock.Now()}
	for _, probe := range busy {
		d.members = append(d.members, &member{name: probe.Name, transport: probe.Transport, state: StateWaiting})
	}
	sort.Slice(d.members, func(i, j int) bool { return d.members[i].name < d.members[j].name })
	m.deps.Cron.Hold()
	m.mu.Lock()
	m.cur = d
	m.mu.Unlock()
	for _, mem := range d.members {
		if mem.transport == TransportBridge {
			m.sendPause(ctx, d, mem)
		}
	}
	m.deps.Logf("drain %s: began with %d busy agent(s)", d.id, len(d.members))
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return m.Status(), nil
}

// Status returns a snapshot and renews the drain's lease.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := m.cur
	if d == nil {
		return Status{Phase: PhaseIdle}
	}
	d.lastStatus = m.deps.Clock.Now()
	st := Status{
		ID:             d.id,
		Phase:          d.phase,
		Done:           allSettled(d.members),
		TimeoutMinutes: TimeoutMinutes(d.timeout),
		Warning:        d.warning,
	}
	for _, mem := range d.members {
		st.Members = append(st.Members, MemberStatus{Name: mem.name, Transport: mem.transport, State: mem.state})
	}
	return st
}

// Tick runs one poll: it expires a lapsed lease, and while draining it
// probes each member, delivers pending pause notices and advances states.
func (m *Manager) Tick(ctx context.Context) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.tickLocked(ctx)
}

// Commit ends the drain and persists its set for the next daemon. Without
// force every member must be drained. The cron hold stays on: the process
// is about to be restarted.
func (m *Manager) Commit(ctx context.Context, force bool) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.tickLocked(ctx)
	m.mu.Lock()
	d := m.cur
	if d == nil {
		m.mu.Unlock()
		return ErrNoDrain
	}
	if d.phase == PhaseCommitted {
		m.mu.Unlock()
		return nil
	}
	if !force && !allSettled(d.members) {
		m.mu.Unlock()
		return ErrNotDrained
	}
	agents := make([]string, 0, len(d.members))
	for _, mem := range d.members {
		agents = append(agents, mem.name)
	}
	m.mu.Unlock()
	pending := PendingResume{
		DrainID:       d.id,
		Agents:        agents,
		DeferredTasks: m.deps.Cron.Held(),
		WrittenAt:     m.deps.Clock.Now(),
	}
	if err := m.deps.Store.Write(pending); err != nil {
		return fmt.Errorf("drain: persisting pending resume: %w", err)
	}
	m.mu.Lock()
	d.phase = PhaseCommitted
	m.mu.Unlock()
	m.deps.Logf("drain %s: committed (force=%v)", d.id, force)
	return nil
}

// Cancel ends the drain without a restart: members that got the pause
// notice are told to resume, cron is released and any persisted set is
// removed. It is valid while committed, for a restart that failed.
func (m *Manager) Cancel(ctx context.Context, reason string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.cancelLocked(ctx, reason)
}

// Run ticks every PollInterval while a drain is active, until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	for {
		if !m.active() {
			select {
			case <-ctx.Done():
				return
			case <-m.wake:
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-m.deps.Clock.After(m.deps.PollInterval):
			m.Tick(ctx)
		}
	}
}

func (m *Manager) active() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cur != nil
}

func (m *Manager) tickLocked(ctx context.Context) {
	m.mu.Lock()
	d := m.cur
	if d == nil {
		m.mu.Unlock()
		return
	}
	expired := m.deps.Clock.Now().Sub(d.lastStatus) > m.deps.LeaseTTL
	phase := d.phase
	m.mu.Unlock()
	if expired {
		m.deps.Logf("drain %s: lease expired", d.id)
		_ = m.cancelLocked(ctx, "lease expired")
		return
	}
	if phase != PhaseDraining {
		return
	}
	for _, mem := range d.members {
		if mem.state == StateGone {
			continue
		}
		m.pollMember(ctx, d, mem)
	}
}

// pollMember advances one member by one poll. d's members are only mutated
// under opMu; mu is taken for each write so Status reads a consistent view.
func (m *Manager) pollMember(ctx context.Context, d *activeDrain, mem *member) {
	notifiedNow := false
	if mem.queued {
		held, err := m.deps.Notifier.Holds(mem.name, pauseID(d, mem))
		if err != nil {
			m.deps.Logf("drain %s: outbox check for %s: %v", d.id, mem.name, err)
		} else if !held {
			m.update(func() { mem.queued, mem.state = false, StateNotified })
			notifiedNow = true
		}
	}
	probe, err := m.deps.Prober.Probe(ctx, mem.name)
	if err != nil {
		m.deps.Logf("drain %s: probing %s: %v", d.id, mem.name, err)
		if !probe.Exists {
			return // no information this poll
		}
	}
	if !probe.Exists {
		m.update(func() { mem.state = StateGone })
		return
	}
	m.update(func() { mem.transport = probe.Transport })
	if probe.Busy {
		m.update(func() {
			mem.idleTicks = 0
			if mem.state == StateDrained {
				mem.state = StateNotified
			}
		})
		return
	}
	switch {
	case d.waitOnly:
	case mem.state == StateWaiting && !mem.pauseSent:
		// First idle poll: deliver the pause. The poll that delivers it is
		// not an idle poll after notification, unless the drain just fell
		// back to waiting only.
		m.sendPause(ctx, d, mem)
		if !d.waitOnly {
			return
		}
	case mem.state == StateWaiting, notifiedNow:
		// The pause is still queued, or landed this poll.
		return
	}
	m.update(func() {
		mem.idleTicks++
		if mem.idleTicks >= m.deps.SettlePolls {
			mem.state = StateDrained
		}
	})
}

// sendPause delivers the pause notice to mem. A queued delivery is notified
// later, once the outbox lets it go; a failed one is retried on the next
// idle poll. Without a message path the whole drain falls back to waiting.
func (m *Manager) sendPause(ctx context.Context, d *activeDrain, mem *member) {
	delivery, err := m.deps.Notifier.Message(ctx, mem.name, PauseText(d.timeout), pauseID(d, mem))
	switch {
	case errors.Is(err, ErrNotifierUnavailable):
		m.update(func() {
			d.waitOnly = true
			d.warning = "agent messaging is unavailable (web server disabled): waiting for busy agents to go idle without telling them to pause"
		})
		m.deps.Logf("drain %s: %v; waiting only", d.id, err)
		return
	case err != nil:
		m.deps.Logf("drain %s: pause notice to %s: %v (will retry)", d.id, mem.name, err)
		return
	}
	m.update(func() {
		mem.pauseSent = true
		if delivery.Queued {
			mem.queued = true
			return
		}
		mem.state = StateNotified
	})
}

func (m *Manager) cancelLocked(ctx context.Context, reason string) error {
	m.mu.Lock()
	d := m.cur
	m.cur = nil
	m.mu.Unlock()
	if d == nil {
		return ErrNoDrain
	}
	m.deps.Logf("drain %s: cancelled (%s)", d.id, reason)
	for _, mem := range d.members {
		if !mem.pauseSent || mem.state == StateGone {
			continue
		}
		if _, err := m.deps.Notifier.Message(ctx, mem.name, CancelText, ""); err != nil {
			m.deps.Logf("drain %s: cancel notice to %s: %v", d.id, mem.name, err)
		}
	}
	m.deps.Cron.Release()
	if err := m.deps.Store.Remove(); err != nil {
		return fmt.Errorf("drain: removing pending resume: %w", err)
	}
	return nil
}

func (m *Manager) update(f func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f()
}

func allSettled(members []*member) bool {
	for _, mem := range members {
		if !mem.settled() {
			return false
		}
	}
	return true
}

func pauseID(d *activeDrain, mem *member) string {
	return "drain-" + d.id + "-" + mem.name
}

func newDrainID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("drain: generating id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
