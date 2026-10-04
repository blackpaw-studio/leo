package bridge

// Router resolves an agent name to the key its claude's mod connects under
// (LEO_BRIDGE_AGENT, fixed at launch, so it can differ from a renamed
// agent's current name) and says whether that bridge is live. The zero and
// nil Router route nothing, so call sites need no nil checks.
type Router struct {
	Hub *Hub
	// Keys maps an agent name to its live launch's bridge key.
	Keys func(agent string) (key string, ok bool)
}

// Key returns agent's bridge key whether or not its bridge is connected.
func (r *Router) Key(agent string) (string, bool) {
	if r == nil || r.Keys == nil {
		return "", false
	}
	key, ok := r.Keys(agent)
	return key, ok && key != ""
}

// Route returns agent's bridge key when its mod is connected right now.
func (r *Router) Route(agent string) (string, bool) {
	key, ok := r.Key(agent)
	if !ok || !r.Connected(key) {
		return "", false
	}
	return key, true
}

// Connected reports whether key's stream is connected.
func (r *Router) Connected(key string) bool {
	return r != nil && r.Hub != nil && r.Hub.Connected(key)
}
