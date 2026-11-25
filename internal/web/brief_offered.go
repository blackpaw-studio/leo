package web

import "sync"

// briefOffers remembers, per launch, whether the launch's MCP server is
// currently offering leo_brief. Brief guidance tells the model to use that
// tool, so it is injected only while the session actually has it: the
// daemon's brief.enabled switch alone says nothing about a session launched
// before the switch moved, or by a leo binary that predates the feature.
//
// The state is keyed by the launch's attention token, not the agent name: the
// token's mapping to an agent moves with a live rename (AttentionStore.Move),
// so the state follows it, and a relaunch holds a new token and starts from
// "not offered". It is in memory only: a daemon restart forgets it, and every
// MCP server reports again when its event stream reconnects.
type briefOffers struct {
	mu    sync.Mutex
	byTok map[string]struct{}
}

// set records what the MCP server of the launch holding token is offering.
// Not offered is the absence of an entry, so withdrawing frees it.
func (b *briefOffers) set(token string, offered bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !offered {
		delete(b.byTok, token)
		return
	}
	if b.byTok == nil {
		b.byTok = map[string]struct{}{}
	}
	b.byTok[token] = struct{}{}
}

// offered reports whether the launch holding token last reported leo_brief
// as offered. A launch that never reported is not offering it.
func (b *briefOffers) offered(token string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.byTok[token]
	return ok
}
