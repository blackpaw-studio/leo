package observe

// bridgeOutstanding is the attention store's B-051 bookkeeping for bridged
// agents: the running children of each agent's live launch, and which
// agent each launch belongs to now.
//
// A launch is named by its bridge key and its launch id (see
// bridge.LaunchID). The key alone is not enough: a key outlives an agent
// (a recreated agent of the same name takes it again), so a dispatch the
// stopped agent left running, counted by the key it was started under,
// would hold the replacement. Dispatches are therefore counted by launch
// id; subagents, which only the live launch's mod reports, by key while
// that launch is bound, and are dropped when it ends, since its dead mod
// can never report them stopping.
//
// The binding lives here, under the store's lock, so a rename (Move) and
// the application of a count or a transition are ordered against each
// other. Resolving names through the router instead would need the
// supervisor's lock under the store's, which the supervisor's own calls
// into the store make a lock-order inversion.
//
// Not safe for concurrent use: the AttentionStore's mutex guards it.
type bridgeOutstanding struct {
	keyOf    map[string]string      // agent name → its live launch's bridge key
	launches map[string]boundLaunch // bridge key → the launch bound to it
	byLaunch map[string]string      // launch id → bridge key, while bound
	// subagents counts the bound launch's subagents by key; dispatches
	// counts every caller launch's dispatches by launch id, bound or not,
	// as the newest dispatch snapshot reported them.
	subagents  map[string]int
	dispatches map[string]int
}

// boundLaunch is the launch a bridge key belongs to and its agent.
type boundLaunch struct {
	launch, agent string
}

func newBridgeOutstanding() bridgeOutstanding {
	return bridgeOutstanding{
		keyOf:      map[string]string{},
		launches:   map[string]boundLaunch{},
		byLaunch:   map[string]string{},
		subagents:  map[string]int{},
		dispatches: map[string]int{},
	}
}

// bind files the launch under key as agent's, replacing whatever launch
// either had: the replaced launches' subagents go with them.
func (b *bridgeOutstanding) bind(key, launch, agent string) {
	if prev, ok := b.launches[key]; ok {
		b.unbind(prev.agent)
	}
	b.unbind(agent)
	b.keyOf[agent] = key
	b.launches[key] = boundLaunch{launch: launch, agent: agent}
	b.byLaunch[launch] = key
}

// unbind drops agent's launch, if it has one, and that launch's subagents.
func (b *bridgeOutstanding) unbind(agent string) {
	key, ok := b.keyOf[agent]
	if !ok {
		return
	}
	delete(b.keyOf, agent)
	delete(b.byLaunch, b.launches[key].launch)
	delete(b.launches, key)
	delete(b.subagents, key)
}

// unbindLaunch unbinds key if launch is still the launch bound to it.
func (b *bridgeOutstanding) unbindLaunch(key, launch string) (agent string, ok bool) {
	bl, bound := b.launches[key]
	if !bound || bl.launch != launch {
		return "", false
	}
	b.unbind(bl.agent)
	return bl.agent, true
}

// move re-files oldName's launch under newName. A launch newName already
// has wins: only a launch bound after the supervisor's rename (so newer
// than oldName's) can have made it.
func (b *bridgeOutstanding) move(oldName, newName string) {
	key, ok := b.keyOf[oldName]
	if !ok {
		return
	}
	if _, taken := b.keyOf[newName]; taken {
		b.unbind(oldName)
		return
	}
	delete(b.keyOf, oldName)
	b.keyOf[newName] = key
	bl := b.launches[key]
	bl.agent = newName
	b.launches[key] = bl
}

// agent returns the agent key's bound launch belongs to now.
func (b *bridgeOutstanding) agent(key string) (string, bool) {
	bl, ok := b.launches[key]
	return bl.agent, ok
}

// boundAgent returns the agent key belongs to while launch is the launch
// bound to it: a report from any other launch of the key (one that retired
// after the hub accepted it) has no agent.
func (b *bridgeOutstanding) boundAgent(key, launch string) (string, bool) {
	bl, ok := b.launches[key]
	if !ok || bl.launch != launch {
		return "", false
	}
	return bl.agent, true
}

// launchAgent returns the agent launch belongs to now, if it is bound.
func (b *bridgeOutstanding) launchAgent(launch string) (string, bool) {
	key, ok := b.byLaunch[launch]
	if !ok {
		return "", false
	}
	return b.agent(key)
}

// of returns agent's outstanding children; zero for an unbound agent.
func (b *bridgeOutstanding) of(agent string) Outstanding {
	key, ok := b.keyOf[agent]
	if !ok {
		return Outstanding{}
	}
	return Outstanding{Dispatches: b.dispatches[b.launches[key].launch], Subagents: b.subagents[key]}
}

// setSubagents records launch's subagents while it is the launch bound to
// key, reporting whether that changed anything. Another launch's report is
// ignored: it ended (its children with it), or (binding precedes the key's
// use) never existed.
func (b *bridgeOutstanding) setSubagents(key, launch string, n int) bool {
	if _, bound := b.boundAgent(key, launch); !bound {
		return false
	}
	return setCount(b.subagents, key, n)
}

// setDispatches records launch's dispatches, reporting whether that
// changed anything.
func (b *bridgeOutstanding) setDispatches(launch string, n int) bool {
	return setCount(b.dispatches, launch, n)
}

// dispatchLaunches returns every launch with outstanding dispatches.
func (b *bridgeOutstanding) dispatchLaunches() []string {
	launches := make([]string, 0, len(b.dispatches))
	for launch := range b.dispatches {
		launches = append(launches, launch)
	}
	return launches
}

// setCount sets m[k] to n (0 deletes it), reporting whether it changed.
func setCount(m map[string]int, k string, n int) bool {
	n = max(n, 0)
	if m[k] == n {
		return false
	}
	if n == 0 {
		delete(m, k)
	} else {
		m[k] = n
	}
	return true
}

func (o Outstanding) total() int { return o.Dispatches + o.Subagents }
