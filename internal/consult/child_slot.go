package consult

import "context"

// MaxDispatchDepth is how many generations of dispatches may nest below a root
// dispatch: a root's child is depth 1, and a request that would land deeper is
// refused. Without a cap a runaway subagent can fan out without bound.
const MaxDispatchDepth = 4

// MaxLiveDescendantsPerRoot bounds the live (not finished) dispatches and
// consults below one root dispatch, at any depth. Children of a live dispatch
// take no slot from the shared pool, so this is what bounds a tree.
const MaxLiveDescendantsPerRoot = 6

// limiter is the slot pool a run draws from: the shared one, or the unlimited
// stand-in for a run started under a live dispatch.
func (d *Dispatcher) limiter(exempt bool) *slotLimiter {
	if exempt {
		return d.exempt
	}
	return d.slots
}

func (d *Dispatcher) slotsFor(s *runState) *slotLimiter { return d.limiter(s.slotExempt) }

// newRunState builds the in-memory state of a run from its record. Every
// construction goes through it so slotExempt always follows the one persisted
// source, Record.SlotExempt: acquire and release then pick the same limiter,
// for a fresh, a restored and a continued run alike.
func newRunState(rec Record, handle Handle, done chan struct{}, cancel context.CancelFunc) *runState {
	return &runState{record: rec, handle: handle, done: done, cancel: cancel, slotExempt: rec.SlotExempt}
}

// admitNested vets a start under parentID (empty for a top-level start, which
// is always admitted and uses the shared pool exactly as before). It refuses
// a start nested deeper than MaxDispatchDepth, or into a root that already has
// MaxLiveDescendantsPerRoot live descendants (dispatches and consults, any
// depth). Over a cap it rejects rather than queues: queueing inside one tree
// can deadlock, since children waiting on grandchildren would fill it. The
// result reports whether the parent is live, i.e. whether the new run takes
// no pool slot. Callers hold nestMu.
func (d *Dispatcher) admitNested(parentID string) (exempt bool, err error) {
	if parentID == "" {
		return false, nil
	}
	if err := d.checkDepth(parentID); err != nil {
		return false, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	root := d.rootOfLocked(parentID)
	live := 0
	for id, state := range d.runs {
		if id != root && !state.record.Status.Terminal() && d.rootOfLocked(id) == root {
			live++
		}
	}
	if live >= MaxLiveDescendantsPerRoot {
		return false, invalidf("dispatch descendant limit (%d) reached: root dispatch %s already has %d live descendants (dispatches and consults, at any depth); wait for one to finish or do the work in the current subagent", MaxLiveDescendantsPerRoot, root, live)
	}
	parent := d.runs[parentID]
	return parent != nil && !parent.record.Status.Terminal(), nil
}

// rootOfLocked follows the parent chain of the runs the dispatcher holds up to
// the dispatch that has none (or the last one it can see).
func (d *Dispatcher) rootOfLocked(id string) string {
	for range MaxDispatchDepth + 1 {
		state := d.runs[id]
		if state == nil || state.record.ParentDispatchID == "" {
			return id
		}
		id = state.record.ParentDispatchID
	}
	return id
}

// checkDepth refuses a child of parentID that would exceed MaxDispatchDepth.
func (d *Dispatcher) checkDepth(parentID string) error {
	depth, id := 0, parentID
	for id != "" && ValidDispatchID(id) && depth <= MaxDispatchDepth {
		rec, _, err := d.lookup(id)
		if err != nil || rec.Kind != "dispatch" {
			break
		}
		depth++
		id = rec.ParentDispatchID
	}
	if depth > MaxDispatchDepth {
		return invalidf("dispatch nesting depth limit (%d) reached: a dispatch started by %s would be nested more than %d levels below its root; do the work in the current subagent instead", MaxDispatchDepth, parentID, MaxDispatchDepth)
	}
	return nil
}
