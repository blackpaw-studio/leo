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
	// noticeTimeout bounds one pause or cancel notice. It runs on its own
	// context: the IPC request that started it may already have returned.
	noticeTimeout = 2 * time.Minute
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

	// Go runs a pause or cancel notice off the caller's goroutine (default:
	// a new goroutine); tests make it synchronous.
	Go func(func())

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
	// sending: a pause notice is in flight; nothing else is sent until it
	// settles.
	sending bool
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
// that probe (Begin, Tick, Commit, Cancel); mu guards all drain and member
// state, which notices in flight also update, so Status never waits on a
// probe and no operation waits on a notice.
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
	if deps.Go == nil {
		deps.Go = func(f func()) { go f() }
	}
	return &Manager{deps: deps, wake: make(chan struct{}, 1)}
}

// Begin starts a drain over the agents that are busy now, holds cron, and
// starts the pause notices to bridge members without waiting on them: one
// delivery can take seconds, and Begin answers an IPC call. An empty set is
// done at once.
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
	var bridged []*member
	m.mu.Lock()
	m.cur = d
	for _, mem := range d.members {
		if mem.transport == TransportBridge {
			mem.sending = true
			bridged = append(bridged, mem)
		}
	}
	m.mu.Unlock()
	m.deps.Logf("drain %s: began with %d busy agent(s)", d.id, len(d.members))
	for _, mem := range bridged {
		m.sendPause(d, mem)
	}
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return m.Status(), nil
}

// BusyAgents reports the agents that are busy now, without starting a
// drain.
func (m *Manager) BusyAgents(ctx context.Context) ([]Probe, error) {
	return m.deps.Prober.BusyAgents(ctx)
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
// probes each member, starts pending pause notices and advances states.
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
// notice are told to resume (in the background, like the pause), cron is
// released and any persisted set is removed. It is valid while committed,
// for a restart that failed.
func (m *Manager) Cancel(_ context.Context, reason string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.cancelLocked(reason)
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
		_ = m.cancelLocked("lease expired")
		return
	}
	if phase != PhaseDraining {
		return
	}
	for _, mem := range d.members {
		m.pollMember(ctx, d, mem)
	}
}

// pollMember advances one member by one poll. The outbox check and the
// probe run unlocked; every read and write of member state holds mu,
// because a notice in flight may settle at any moment.
func (m *Manager) pollMember(ctx context.Context, d *activeDrain, mem *member) {
	m.mu.Lock()
	gone, queued := mem.state == StateGone, mem.queued
	m.mu.Unlock()
	if gone {
		return
	}
	notifiedNow := false
	if queued {
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
	m.mu.Lock()
	startPause := m.applyProbe(d, mem, probe, notifiedNow)
	m.mu.Unlock()
	if startPause {
		m.sendPause(d, mem)
	}
}

// applyProbe advances mem from one probe and reports whether to start its
// pause notice (mem.sending is then already set). Caller holds mu.
func (m *Manager) applyProbe(d *activeDrain, mem *member, probe Probe, notifiedNow bool) (startPause bool) {
	if !probe.Exists {
		mem.state = StateGone
		return false
	}
	mem.transport = probe.Transport
	if probe.Busy {
		mem.idleTicks = 0
		if mem.state == StateDrained {
			mem.state = StateNotified
		}
		return false
	}
	switch {
	case d.waitOnly:
	case mem.state == StateWaiting && !mem.pauseSent && !mem.sending:
		// First idle poll: start the pause. Polls count toward settling
		// only once it has landed.
		mem.sending = true
		return true
	case mem.state == StateWaiting, notifiedNow:
		// The pause is in flight or still queued, or landed this poll.
		return false
	}
	mem.idleTicks++
	if mem.idleTicks >= m.deps.SettlePolls {
		mem.state = StateDrained
	}
	return false
}

// sendPause delivers the pause notice to mem through deps.Go; the caller
// has set mem.sending. A delivered notice notifies mem; a queued one
// notifies it once the outbox lets it go; a failed one is retried on the
// next idle poll. Without a message path the whole drain falls back to
// waiting. A notice that lands after its drain was cancelled is followed
// by a cancel, since Cancel could not know it would land.
func (m *Manager) sendPause(d *activeDrain, mem *member) {
	text, id := PauseText(d.timeout), pauseID(d, mem)
	m.deps.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), noticeTimeout)
		defer cancel()
		delivery, err := m.deps.Notifier.Message(ctx, mem.name, text, id)
		m.mu.Lock()
		mem.sending = false
		cancelled := m.cur != d
		switch {
		case errors.Is(err, ErrNotifierUnavailable):
			d.waitOnly = true
			d.warning = "agent messaging is unavailable (web server disabled): waiting for busy agents to go idle without telling them to pause"
		case err == nil:
			mem.pauseSent = true
			if delivery.Queued {
				mem.queued = true
			} else {
				mem.state = StateNotified
			}
		}
		m.mu.Unlock()
		switch {
		case errors.Is(err, ErrNotifierUnavailable):
			m.deps.Logf("drain %s: %v; waiting only", d.id, err)
		case err != nil:
			m.deps.Logf("drain %s: pause notice to %s: %v (will retry)", d.id, mem.name, err)
		case cancelled:
			m.sendCancel(d.id, mem.name)
		}
	})
}

// sendCancel tells name the restart is off, through deps.Go.
func (m *Manager) sendCancel(drainID, name string) {
	m.deps.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), noticeTimeout)
		defer cancel()
		if _, err := m.deps.Notifier.Message(ctx, name, CancelText, ""); err != nil {
			m.deps.Logf("drain %s: cancel notice to %s: %v", drainID, name, err)
		}
	})
}

func (m *Manager) cancelLocked(reason string) error {
	m.mu.Lock()
	d := m.cur
	m.cur = nil
	var notify []string
	if d != nil {
		for _, mem := range d.members {
			if mem.pauseSent && mem.state != StateGone {
				notify = append(notify, mem.name)
			}
		}
	}
	m.mu.Unlock()
	if d == nil {
		return ErrNoDrain
	}
	m.deps.Logf("drain %s: cancelled (%s)", d.id, reason)
	for _, name := range notify {
		m.sendCancel(d.id, name)
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
