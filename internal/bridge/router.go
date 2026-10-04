package bridge

// Router resolves an agent name to the generation its claude's mod connects
// under (LEO_BRIDGE_AGENT, fixed at launch, so its key can differ from a
// renamed agent's current name) and says whether that bridge is live. The
// zero and nil Router route nothing, so call sites need no nil checks.
type Router struct {
	Hub *Hub
	// Targets maps an agent name to its live launch's generation.
	Targets func(agent string) (target Target, ok bool)
	// Queue, if set, queues a deliver from agent from ("" for none) for
	// agent's generation t durably: kept until the mod acks it, and carried
	// over to the agent's next launch should t end first. Without it a
	// deliver is queued in the hub alone (see Deliver).
	Queue func(agent string, t Target, cmd Command, from string) (*Ticket, error)
}

// Deliver queues cmd, a deliver from agent from, for agent's generation t
// (from Route). The ticket settles with the mod's ack.
func (r *Router) Deliver(agent string, t Target, cmd Command, from string) (*Ticket, error) {
	if r.Queue != nil {
		return r.Queue(agent, t, cmd, from)
	}
	return r.Hub.EnqueueTo(t, cmd)
}

// IsDurable reports whether Deliver keeps a deliver past its generation:
// one whose generation ends (ErrForgotten) or whose daemon closes the hub
// (ErrClosed) first is not lost but waits for the agent's next launch.
func (r *Router) IsDurable() bool { return r != nil && r.Queue != nil }

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
