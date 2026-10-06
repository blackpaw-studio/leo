package consult

import (
	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/harness"
)

// bridgedUsage is what a bridged interactive dispatch's mod has reported of
// its usage: the claude session's running cost, and the token counts its
// turns reported one by one, summed.
type bridgedUsage struct {
	cost *float64
	// input counts cached reads and writes too, as the headless path does.
	input, output int64
	// hasTokens: some turn reported its tokens. isShort: some turn ended
	// without them, or a sum overflowed, so the counts fall short.
	hasTokens, isShort bool
	// seen holds the event ids of the turns already counted, so a replayed
	// report is not counted twice; order bounds it.
	seen  map[string]bool
	order []string
}

// usage is the invocation snapshot the reports so far make: complete only
// once every turn reported its tokens and the session cost is known.
func (u *bridgedUsage) usage() *harness.Usage {
	out := &harness.Usage{CostUSD: clonePtr(u.cost), Incomplete: !u.hasTokens || u.isShort || u.cost == nil}
	if u.hasTokens {
		out.InputTokens, out.OutputTokens = harness.Int64(u.input), harness.Int64(u.output)
	}
	return out
}

// isReplay reports whether eventID was counted already, and remembers it.
// An empty id cannot be told from another, so it always counts.
func (u *bridgedUsage) isReplay(eventID string) bool {
	if eventID == "" {
		return false
	}
	if u.seen[eventID] {
		return true
	}
	if u.seen == nil {
		u.seen = map[string]bool{}
	}
	u.seen[eventID] = true
	u.order = append(u.order, eventID)
	if len(u.order) > maxInteractiveDedup {
		delete(u.seen, u.order[0])
		u.order = u.order[1:]
	}
	return false
}

func (u *bridgedUsage) add(t bridge.TurnTokens) {
	in, ok := harness.AddInt64(t.Input, t.CacheRead)
	if ok {
		in, ok = harness.AddInt64(in, t.CacheCreation)
	}
	if ok {
		in, ok = harness.AddInt64(u.input, in)
	}
	out, outOK := harness.AddInt64(u.output, t.Output)
	if !ok || !outOK {
		u.isShort = true
		return
	}
	u.input, u.output, u.hasTokens = in, out, true
}

// ApplyBridgeTokens adds one completed bridged turn's tokens (nil when its
// mod reported none) to an interactive dispatch's usage. eventID is the
// turn.complete's, so a replay counts once. Tokens for an unknown or
// settled run are ignored.
func (d *Dispatcher) ApplyBridgeTokens(id, eventID string, tokens *bridge.TurnTokens) {
	_, s, err := d.lookup(id)
	if err != nil || s == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if s.record.Mode != ModeInteractive || s.record.Status.Terminal() || s.bridgedUsage.isReplay(eventID) {
		return
	}
	if tokens == nil {
		s.bridgedUsage.isShort = true
	} else {
		s.bridgedUsage.add(*tokens)
	}
	d.applyBridgedUsageLocked(s)
}

func (d *Dispatcher) applyBridgedUsageLocked(s *runState) {
	if len(s.record.UsageInvocations) == 0 {
		d.beginUsageInvocationLocked(s)
	}
	d.applyUsageLocked(s, s.bridgedUsage.usage(), false)
}
