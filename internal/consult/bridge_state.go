package consult

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

// bridgeRecentAfterEnd is how long a finished dispatch stays in its caller's
// roster band.
const bridgeRecentAfterEnd = 60 * time.Second

// BridgeDispatchStates returns the dispatches the claude under bridge key
// asked for, as its mod shows them: every live one, and each that finished
// within bridgeRecentAfterEnd of now, oldest first.
func BridgeDispatchStates(records []Record, key string, now time.Time) []bridge.DispatchState {
	return bridgeDispatchStates(WithChildWait(records), key, now)
}

// bridgeDispatchStates is BridgeDispatchStates for records already run
// through WithChildWait, which a caller serving many keys does once. A
// child may belong to another caller, so wait state is derived from every
// record before a key's own are picked out.
func bridgeDispatchStates(records []Record, key string, now time.Time) []bridge.DispatchState {
	if key == "" {
		return nil
	}
	mine := make([]Record, 0, len(records))
	for _, rec := range records {
		if isBridgeRosterRecord(rec, key, now) {
			mine = append(mine, rec)
		}
	}
	sort.Slice(mine, func(i, j int) bool {
		if mine[i].StartedAt.Equal(mine[j].StartedAt) {
			return mine[i].ID < mine[j].ID
		}
		return mine[i].StartedAt.Before(mine[j].StartedAt)
	})
	states := make([]bridge.DispatchState, 0, len(mine))
	for _, rec := range mine {
		states = append(states, bridgeDispatchState(rec, now))
	}
	return states
}

func isBridgeRosterRecord(rec Record, key string, now time.Time) bool {
	if rec.Kind != "dispatch" || rec.CallerBridgeKey != key || rec.Status == StatusReleased {
		return false
	}
	return !rec.Status.Terminal() || rec.EndedAt.IsZero() || now.Sub(rec.EndedAt) < bridgeRecentAfterEnd
}

func bridgeDispatchState(rec Record, now time.Time) bridge.DispatchState {
	return bridge.DispatchState{
		ID: rec.ID, Name: rec.Name, Role: rec.Role, Template: rec.Template, Model: rec.Model, Effort: rec.Effort, ObservedEffort: rec.ObservedEffort,
		Status: string(rec.Status), Stalled: isStalled(rec, now), ActiveSeconds: rec.LiveActiveSeconds(now),
		TokensIn: clonePtr(rec.InputTokens), TokensOut: clonePtr(rec.OutputTokens), CostUSD: clonePtr(rec.CostUSD),
		Pending: rec.PendingWork.Summary(),
	}
}

// isStalled reports whether any of rec's open turns shows no sign of life
// for its threshold, as leo_wait reports it: a newer finished turn does not
// hide an older one that has gone quiet.
func isStalled(rec Record, now time.Time) bool {
	if rec.Status.Terminal() {
		return false
	}
	for _, t := range rec.Turns {
		if turnStalled(rec, t, now) {
			return true
		}
	}
	return false
}

// turnStalled reports whether t is open with no hook activity for its
// threshold. A turn waiting on background work is quiet by design until that
// work wakes it, so it gets the longer waitingStalledAfter.
func turnStalled(rec Record, t Turn, now time.Time) bool {
	if t.Outcome != "" {
		return false
	}
	activity := rec.HookActivity
	if activity.IsZero() {
		activity = t.StartedAt
	}
	after := stalledAfter
	if t.Pending != nil || rec.Status == StatusWaiting {
		after = waitingStalledAfter
	}
	return !activity.IsZero() && now.Sub(activity) >= after
}

// StateHub is the part of the bridge hub a StatePusher drives.
type StateHub interface {
	ConnectedKeys() []string
	State(key string) bridge.State
	SetState(key string, s bridge.StateSnapshot) error
}

// StatePusher keeps every connected mod's state current: on each Tick it
// builds each connected key's snapshot and pushes it when it differs from
// the last one that key's generation got. Working time alone moving on is
// no change (the mod runs its own clock), so a running dispatch does not
// push every tick. Tick once a second for the 1 s debounce.
type StatePusher struct {
	Hub StateHub
	// Records returns the dispatcher's current records.
	Records func() []Record
	// Delegation returns the policy agents get (dispatches get it off).
	Delegation func() bridge.DelegationState
	Now        func() time.Time
	// Cutoffs hides what a caller's /clear left finished; nil hides nothing.
	Cutoffs *RosterCutoffs

	mu   sync.Mutex
	last map[string]pushedState
}

type pushedState struct {
	gen  uint64
	snap bridge.StateSnapshot // ActiveSeconds zeroed: compared, never sent
}

// Tick pushes every connected key's snapshot that changed.
func (p *StatePusher) Tick() {
	keys := p.Hub.ConnectedKeys()
	if len(keys) == 0 {
		return
	}
	now := p.Now()
	records := WithChildWait(p.Cutoffs.Visible(p.Records()))
	delegation := p.Delegation()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil {
		p.last = map[string]pushedState{}
	}
	live := make(map[string]bool, len(keys))
	for _, key := range keys {
		live[key] = true
		p.pushLocked(key, p.snapshot(key, records, delegation, now))
	}
	for key := range p.last {
		if !live[key] {
			delete(p.last, key)
		}
	}
}

func (p *StatePusher) snapshot(key string, records []Record, delegation bridge.DelegationState, now time.Time) bridge.StateSnapshot {
	if strings.HasPrefix(key, dispatchBridgeKeyPrefix) {
		delegation = bridge.DelegationState{}
	}
	return bridge.StateSnapshot{Delegation: delegation, Dispatches: bridgeDispatchStates(records, key, now)}
}

func (p *StatePusher) pushLocked(key string, snap bridge.StateSnapshot) {
	gen := p.Hub.State(key).Gen
	compared := withoutActiveSeconds(snap)
	if prev, ok := p.last[key]; ok && prev.gen == gen && reflect.DeepEqual(prev.snap, compared) {
		return
	}
	if err := p.Hub.SetState(key, snap); err != nil {
		fmt.Fprintf(os.Stderr, "bridge: pushing state to %s: %v\n", key, err)
		return
	}
	p.last[key] = pushedState{gen: gen, snap: compared}
}

func withoutActiveSeconds(s bridge.StateSnapshot) bridge.StateSnapshot {
	dispatches := make([]bridge.DispatchState, len(s.Dispatches))
	for i, d := range s.Dispatches {
		d.ActiveSeconds = 0
		dispatches[i] = d
	}
	s.Dispatches = dispatches
	return s
}

// RunRecords returns copies of the dispatcher's in-memory records: every
// live run and the most recent finished ones. Unlike Records it never reads
// the disk, so it is cheap enough to call every second.
func (d *Dispatcher) RunRecords() []Record {
	d.mu.Lock()
	defer d.mu.Unlock()
	records := make([]Record, 0, len(d.runs))
	for _, state := range d.runs {
		records = append(records, cloneRecord(state.record))
	}
	return records
}

// CancelForCaller cancels dispatch id for the claude under bridge key, as
// leo_cancel does, but only when that claude asked for it. Any other
// dispatch, one unknown, or an empty key is refused with
// bridge.ErrRequestDenied, without saying which.
func (d *Dispatcher) CancelForCaller(key, id string) error {
	rec, err := d.Get(id)
	if err != nil || key == "" || rec.CallerBridgeKey != key {
		return fmt.Errorf("%w: %s is not a dispatch of %s", bridge.ErrRequestDenied, id, key)
	}
	if _, err := d.Cancel(id); err != nil {
		return fmt.Errorf("canceling %s: %w", id, err)
	}
	return nil
}

// BridgeRequestHandler serves mods' requests against this dispatcher.
func (d *Dispatcher) BridgeRequestHandler() bridge.RequestHandler {
	return func(agent, op, dispatchID string) error {
		if op != bridge.RequestDispatchCancel {
			return fmt.Errorf("unknown bridge request %q", op)
		}
		return d.CancelForCaller(agent, dispatchID)
	}
}
