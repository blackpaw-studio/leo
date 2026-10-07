package observe

// bridgeOutstanding is the attention store's B-051 bookkeeping for bridged
// agents: each agent's running children counted by the bridge key of the
// launch that started them, and which agent each key belongs to now.
//
// Counts are keyed by bridge key because their sources (the dispatch
// store's caller key, the mod's subagent reports) only know the key, and a
// key never changes for a launch while its agent's name can. The key↔name
// binding lives here, under the store's lock, so a rename (Move) and the
// application of a count are ordered against each other: a count read
// before a rename lands on the agent after it. Resolving names through the
// router instead would need the supervisor's lock under the store's, which
// the supervisor's own calls into the store make a lock-order inversion.
//
// Not safe for concurrent use: the AttentionStore's mutex guards it.
type bridgeOutstanding struct {
	keyOf   map[string]string // agent name → its live launch's bridge key
	agentOf map[string]string // bridge key → agent name
	counts  map[string]Outstanding
}

func newBridgeOutstanding() bridgeOutstanding {
	return bridgeOutstanding{keyOf: map[string]string{}, agentOf: map[string]string{}, counts: map[string]Outstanding{}}
}

// bind files key under agent, replacing whatever either was bound to.
func (b *bridgeOutstanding) bind(key, agent string) {
	if prev, ok := b.agentOf[key]; ok {
		delete(b.keyOf, prev)
	}
	b.unbind(agent)
	b.keyOf[agent] = key
	b.agentOf[key] = agent
}

// unbind drops agent's binding, keeping its key's counts.
func (b *bridgeOutstanding) unbind(agent string) {
	if key, ok := b.keyOf[agent]; ok {
		delete(b.agentOf, key)
		delete(b.keyOf, agent)
	}
}

// move re-files oldName's binding under newName. A binding newName already
// has wins: only a launch registered after the supervisor's rename (so
// newer than oldName's) can have made it.
func (b *bridgeOutstanding) move(oldName, newName string) {
	key, ok := b.keyOf[oldName]
	if !ok {
		return
	}
	delete(b.keyOf, oldName)
	if _, taken := b.keyOf[newName]; taken {
		delete(b.agentOf, key)
		return
	}
	b.keyOf[newName] = key
	b.agentOf[key] = newName
}

// agent returns the agent key belongs to now.
func (b *bridgeOutstanding) agent(key string) (string, bool) {
	name, ok := b.agentOf[key]
	return name, ok
}

// of returns agent's outstanding children; zero for an unbound agent.
func (b *bridgeOutstanding) of(agent string) Outstanding {
	key, ok := b.keyOf[agent]
	if !ok {
		return Outstanding{}
	}
	return b.counts[key]
}

// set applies one source's count to key, reporting whether it changed.
func (b *bridgeOutstanding) set(key string, apply func(*Outstanding)) bool {
	prev := b.counts[key]
	next := prev
	apply(&next)
	if next == prev {
		return false
	}
	if next.total() == 0 {
		delete(b.counts, key)
	} else {
		b.counts[key] = next
	}
	return true
}

// dispatchKeys returns every key with outstanding dispatches.
func (b *bridgeOutstanding) dispatchKeys() []string {
	var keys []string
	for key, o := range b.counts {
		if o.Dispatches > 0 {
			keys = append(keys, key)
		}
	}
	return keys
}

func (o Outstanding) total() int { return o.Dispatches + o.Subagents }
