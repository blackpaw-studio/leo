package service

import (
	"fmt"
	"os"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/harness/claude/bridgemod"
)

// ReserveAdoptions holds, for each agent in names whose surviving tmux
// session a restarted daemon is about to adopt, the bridge key that
// session's mod connects under, before any agent launches. Without it a
// fresh launch could take the key (a new agent named after a renamed
// agent's launch-time key) and the adoption would then take it back from
// under that launch. It also settles the hub's adoption wait: only these
// keys' mods are told to retry until adopted (see bridge.Hub.AwaitAdoption).
// Called once per daemon boot, by RestoreAgents.
func (s *Supervisor) ReserveAdoptions(names []string) {
	keys := make([]string, 0, len(names))
	s.mu.Lock()
	for _, name := range names {
		key, _, ok := tmuxSessionBridge(s.tmuxPath, agent.SessionName(name))
		if !ok {
			continue // a legacy session: nothing to hold
		}
		s.adoptionKeys[key] = name
		keys = append(keys, key)
	}
	w := s.bridge
	s.mu.Unlock()
	if w != nil {
		w.hub.AwaitAdoption(keys)
	}
}

// ReleaseAdoption gives back what agent name reserved and will not adopt
// (its session ended, it is adopted legacy, or its spawn failed): its key
// is free again, and its old mod is told to stop.
func (s *Supervisor) ReleaseAdoption(name string) {
	s.mu.Lock()
	var released []string
	for key, owner := range s.adoptionKeys {
		if owner == name {
			delete(s.adoptionKeys, key)
			released = append(released, key)
		}
	}
	w := s.bridge
	s.mu.Unlock()
	if w == nil {
		return
	}
	for _, key := range released {
		w.hub.EndAdoption(key)
	}
}

// renameAdoptionLocked moves oldName's reservations to newName. Caller
// holds s.mu.
func (s *Supervisor) renameAdoptionLocked(oldName, newName string) {
	for key, owner := range s.adoptionKeys {
		if owner == oldName {
			s.adoptionKeys[key] = newName
		}
	}
}

// adoptBridge re-opens the generation of key, bound to launch, for id's
// adopted session, whose mod reconnects under both. A key another agent's
// launch already holds is never taken from it: the session is adopted
// legacy instead, and the holder keeps its generation and its messages.
func (s *Supervisor) adoptBridge(id *procIdentity, key, launch, conversation string) bridgeLaunch {
	w := s.bridgeWiring()
	if w == nil {
		id.setLegacy()
		return bridgeLaunch{}
	}
	s.mu.RLock()
	held := s.bridgeKeyHeldLocked(key, id)
	s.mu.RUnlock()
	if held {
		fmt.Fprintf(os.Stderr, "[%s] warning: another agent's launch holds leo bridge key %s; the adopted session runs without the bridge\n", id.Name(), key)
		id.setLegacy()
		s.ReleaseAdoption(id.Name())
		return bridgeLaunch{}
	}
	target, err := w.hub.Open(key, launch)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] warning: adopting the leo bridge %s: %v\n", id.Name(), key, err)
		id.setLegacy()
		s.ReleaseAdoption(id.Name())
		return bridgeLaunch{}
	}
	id.setBridge(target)
	// The identity holds the key now.
	s.ReleaseAdoption(id.Name())
	return bridgeLaunch{plan: bridgemod.Plan{Key: key, Launch: launch}, bridged: true, adopted: true, target: target, conversation: conversation}
}

// bridgeTarget returns the generation of agent name's live launch, if it
// loaded the bridge.
func (s *Supervisor) bridgeTarget(name string) (bridge.Target, bool) {
	s.mu.RLock()
	id, ok := s.identities[name]
	s.mu.RUnlock()
	if !ok {
		return bridge.Target{}, false
	}
	target := id.BridgeRoute().Target
	return target, target.Key != ""
}
