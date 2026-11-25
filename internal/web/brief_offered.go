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
//
// A SessionStart that restores the stored brief (resume, clear, compact)
// usually fires before the MCP server has reported. The restore is kept as
// pending for the launch and delivered on its next prompt once the tool is
// offered; a report that withdraws the tool, or another SessionStart, drops it.
type briefOffers struct {
	mu      sync.Mutex
	byTok   map[string]struct{}
	pending map[string]string // token → the SessionStart source awaiting its restore
}

// set records what the MCP server of the launch holding token is offering.
// Not offered is the absence of an entry, so withdrawing frees it, and it
// drops any restore still waiting.
func (b *briefOffers) set(token string, offered bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !offered {
		delete(b.byTok, token)
		delete(b.pending, token)
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

// setPending keeps a restore for token's launch ("" source clears it): the
// newest SessionStart is the only one that counts.
func (b *briefOffers) setPending(token, source string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if source == "" {
		delete(b.pending, token)
		return
	}
	if b.pending == nil {
		b.pending = map[string]string{}
	}
	b.pending[token] = source
}

// takePending returns and clears token's waiting restore.
func (b *briefOffers) takePending(token string) (source string, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	source, ok = b.pending[token]
	delete(b.pending, token)
	return source, ok
}

// hasState reports whether anything is held for token.
func (b *briefOffers) hasState(token string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, offered := b.byTok[token]
	_, pending := b.pending[token]
	return offered || pending
}

// prune drops what is held for every token live reports as retired.
func (b *briefOffers) prune(live func(token string) bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for tok := range b.byTok {
		if !live(tok) {
			delete(b.byTok, tok)
		}
	}
	for tok := range b.pending {
		if !live(tok) {
			delete(b.pending, tok)
		}
	}
}
