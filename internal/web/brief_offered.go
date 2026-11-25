package web

import "sync"

// briefOffers remembers, per agent, whether the agent's MCP server is
// currently offering leo_brief. Brief guidance tells the model to use that
// tool, so it is injected only while the session actually has it: the daemon's
// brief.enabled switch alone says nothing about a session launched before the
// switch moved, or by a leo binary that predates the feature.
//
// Each report is bound to the launch token that made it, so one launch's
// report is never read as the next launch's. The state is in memory only: a
// daemon restart forgets it, and every MCP server reports again when its
// event stream reconnects.
type briefOffers struct {
	mu      sync.Mutex
	byAgent map[string]briefOffer
}

type briefOffer struct {
	token   string
	offered bool
}

// set records what the MCP server of agent's launch (token) is offering.
func (b *briefOffers) set(agent, token string, offered bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.byAgent == nil {
		b.byAgent = map[string]briefOffer{}
	}
	b.byAgent[agent] = briefOffer{token: token, offered: offered}
}

// offered reports whether the launch holding token last reported leo_brief
// as offered. A launch that never reported is not offering it.
func (b *briefOffers) offered(agent, token string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	o, ok := b.byAgent[agent]
	return ok && o.token == token && o.offered
}
