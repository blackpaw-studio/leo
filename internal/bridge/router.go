package bridge

// Router resolves an agent name to the generation its claude's mod connects
// under (LEO_BRIDGE_AGENT, fixed at launch, so its key can differ from a
// renamed agent's current name) and says whether that bridge is live. The
// zero and nil Router route nothing, so call sites need no nil checks.
type Router struct {
	Hub *Hub
	// Targets maps an agent name to its live launch's generation.
	Targets func(agent string) (target Target, ok bool)
}

// Key returns agent's bridge key whether or not its bridge is connected.
func (r *Router) Key(agent string) (string, bool) {
	target, ok := r.target(agent)
	return target.Key, ok
}

// Route returns the generation of agent's own launch while its mod is
// connected: never another launch's generation of the same key. Sending to
// it with Hub.SendTo fails, instead of reaching a relaunch, if the agent is
// relaunched in between.
func (r *Router) Route(agent string) (Target, bool) {
	own, ok := r.target(agent)
	if !ok || r.Hub == nil {
		return Target{}, false
	}
	live, ok := r.Hub.Live(own.Key)
	if !ok || live != own {
		return Target{}, false
	}
	return live, true
}

func (r *Router) target(agent string) (Target, bool) {
	if r == nil || r.Targets == nil {
		return Target{}, false
	}
	target, ok := r.Targets(agent)
	return target, ok && target.Key != ""
}

// Connected reports whether key's stream is connected.
func (r *Router) Connected(key string) bool {
	return r != nil && r.Hub != nil && r.Hub.Connected(key)
}
