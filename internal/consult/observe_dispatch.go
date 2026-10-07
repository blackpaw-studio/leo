package consult

import (
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// DispatchObserver projects the dispatcher's records into the observability
// API: the snapshot's dispatches and per-agent outstanding counts, and a
// dispatch_changed event for every record that changed since the last Tick.
// Tick once a second for the 1 s diff.
type DispatchObserver struct {
	records       func() []Record
	publisher     observe.Publisher
	now           func() time.Time
	onOutstanding func(agent string, outstanding int)
	owner         func(key string) (agent string, ok bool)

	mu     sync.Mutex
	last   map[string]observe.Dispatch
	counts map[string]int

	// snapMu orders DispatchSnapshot: each reads the records and takes the
	// next snapGen together, so a later generation never holds older data.
	snapMu  sync.Mutex
	snapGen uint64
}

// DispatchObserverOption configures a DispatchObserver.
type DispatchObserverOption func(*DispatchObserver)

// WithDispatchClock replaces time.Now as the observer's clock.
func WithDispatchClock(now func() time.Time) DispatchObserverOption {
	return func(o *DispatchObserver) { o.now = now }
}

// WithOutstandingListener calls fn from Tick whenever an agent's count of
// outstanding dispatches changes, including when it drops to 0.
func WithOutstandingListener(fn func(agent string, outstanding int)) DispatchObserverOption {
	return func(o *DispatchObserver) { o.onOutstanding = fn }
}

// WithDispatchOwner resolves a caller's bridge key to the agent holding it
// now, for the name-keyed reads (OutstandingDispatches, the Tick
// listener). Only dispatches whose caller has a bridge key count: the key
// is fixed at the caller's launch, so it follows a rename where
// Record.Caller cannot. A keyless dispatch (codex/opencode callers, the
// CLI, a claude without the mod) is still listed but never holds
// attention. Without an owner those reads count nothing; DispatchSnapshot
// needs none.
func WithDispatchOwner(fn func(key string) (agent string, ok bool)) DispatchObserverOption {
	return func(o *DispatchObserver) { o.owner = fn }
}

// NewDispatchObserver observes the records records returns, publishing
// through publisher (nil publishes nothing).
func NewDispatchObserver(records func() []Record, publisher observe.Publisher, opts ...DispatchObserverOption) *DispatchObserver {
	o := &DispatchObserver{records: records, publisher: publisher, now: time.Now}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// Dispatches returns live dispatches plus those ended within
// observe.DispatchLinger of now.
func (o *DispatchObserver) Dispatches(now time.Time) []observe.Dispatch {
	out := []observe.Dispatch{}
	for _, rec := range o.records() {
		if rec.Kind == "dispatch" && withinLinger(rec, now) {
			out = append(out, observedDispatch(rec, now))
		}
	}
	return out
}

// OutstandingDispatches returns each caller agent's count of its own
// non-terminal dispatches, by bridge key (see WithDispatchOwner). A
// dispatch's dispatches count toward that dispatch, not the agent above it.
func (o *DispatchObserver) OutstandingDispatches() map[string]int {
	return o.outstandingCounts(o.records())
}

// DispatchSnapshot returns each caller's count of its own non-terminal
// dispatches keyed by the caller's bridge key, not its name: the attention
// store resolves the key to the agent holding it as it applies the counts,
// so a rename between this read and that cannot misfile them. The
// generation increases in the order the snapshots read the records.
func (o *DispatchObserver) DispatchSnapshot() (uint64, map[string]int) {
	o.snapMu.Lock()
	defer o.snapMu.Unlock()
	counts := map[string]int{}
	for _, rec := range o.records() {
		if outstanding(rec) {
			counts[rec.CallerBridgeKey]++
		}
	}
	o.snapGen++
	return o.snapGen, counts
}

// Tick publishes dispatch_changed for each dispatch whose record changed
// since the last Tick (a long-finished dispatch seen for the first time is
// skipped) and notifies the listener of changed outstanding counts.
func (o *DispatchObserver) Tick() {
	now := o.now()
	records := o.records()
	o.mu.Lock()
	defer o.mu.Unlock()
	o.publishChangesLocked(records, now)
	o.notifyCountsLocked(o.outstandingCounts(records))
}

func (o *DispatchObserver) publishChangesLocked(records []Record, now time.Time) {
	seen := make(map[string]observe.Dispatch, len(records))
	for _, rec := range records {
		if rec.Kind != "dispatch" {
			continue
		}
		d := observedDispatch(rec, now)
		seen[rec.ID] = d
		prev, known := o.last[rec.ID]
		if known && sameDispatch(prev, d) || !known && !withinLinger(rec, now) {
			continue
		}
		if o.publisher != nil {
			o.publisher.Publish(observe.Event{Type: observe.EventDispatchChanged, Payload: &observe.DispatchChangedPayload{Dispatch: d}})
		}
	}
	o.last = seen
}

func (o *DispatchObserver) notifyCountsLocked(counts map[string]int) {
	prev := o.counts
	o.counts = counts
	if o.onOutstanding == nil {
		return
	}
	for agent, n := range counts {
		if prev[agent] != n {
			o.onOutstanding(agent, n)
		}
	}
	for agent, n := range prev {
		if _, still := counts[agent]; !still && n != 0 {
			o.onOutstanding(agent, 0)
		}
	}
}

func (o *DispatchObserver) outstandingCounts(records []Record) map[string]int {
	counts := map[string]int{}
	for _, rec := range records {
		if !outstanding(rec) {
			continue
		}
		if caller, ok := o.keyOwner(rec); ok {
			counts[caller]++
		}
	}
	return counts
}

// outstanding reports whether rec is a running dispatch that holds its
// caller's attention: a direct one by a caller with a bridge key. A
// dispatch's own dispatches count toward that dispatch, not the agent above
// it; a keyless caller's never count.
func outstanding(rec Record) bool {
	return rec.Kind == "dispatch" && !rec.Status.Terminal() && rec.CallerBridgeKey != "" && parentDispatchID(rec) == ""
}

// keyOwner is the agent holding rec's caller bridge key now; ok=false for
// a keyless record or a key no agent holds.
func (o *DispatchObserver) keyOwner(rec Record) (agent string, ok bool) {
	if o.owner == nil || rec.CallerBridgeKey == "" {
		return "", false
	}
	agent, ok = o.owner(rec.CallerBridgeKey)
	return agent, ok && agent != ""
}

func withinLinger(rec Record, now time.Time) bool {
	if !rec.Status.Terminal() {
		return true
	}
	return !rec.EndedAt.IsZero() && now.Sub(rec.EndedAt) < observe.DispatchLinger
}

func parentDispatchID(rec Record) string {
	if id, ok := DispatchIDFromBridgeKey(rec.CallerBridgeKey); ok {
		return id
	}
	return ""
}

func observedDispatch(rec Record, now time.Time) observe.Dispatch {
	d := observe.Dispatch{
		ID: rec.ID, Name: rec.Name, Role: rec.Role, Template: rec.Template, Model: rec.Model,
		Status: string(rec.Status), Stalled: isStalled(rec, now),
		CallerAgent: rec.Caller, ParentDispatchID: parentDispatchID(rec), StartedAt: rec.StartedAt,
		TokensIn: valueOr(rec.InputTokens), TokensOut: valueOr(rec.OutputTokens), CostUSD: valueOr(rec.CostUSD),
	}
	if !rec.EndedAt.IsZero() {
		ended := rec.EndedAt
		d.EndedAt = &ended
	}
	return d
}

func sameDispatch(a, b observe.Dispatch) bool {
	aEnd, bEnd := a.EndedAt, b.EndedAt
	a.EndedAt, b.EndedAt = nil, nil
	if a != b || (aEnd == nil) != (bEnd == nil) {
		return false
	}
	return aEnd == nil || aEnd.Equal(*bEnd)
}

func valueOr[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
