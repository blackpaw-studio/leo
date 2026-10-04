package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

// adoptionSupervisor is a supervisor wired to a hub whose stub tmux has a
// session (any name) launched bridged under key alpha, launch launch-old:
// what agent beta, renamed from alpha while live, left running across a
// daemon restart.
func adoptionSupervisor(t *testing.T) (*Supervisor, *bridge.Hub) {
	t.Helper()
	tmuxPath, _ := statefulTmux(t, "LEO_BRIDGE_AGENT=alpha\nLEO_BRIDGE_LAUNCH=launch-old")
	sv := NewSupervisor(context.Background())
	sv.tmuxPath = tmuxPath
	hub := bridge.New(bridge.Options{})
	t.Cleanup(hub.Close)
	sv.SetBridge(hub, nil, time.Minute)
	return sv, hub
}

func addIdentity(sv *Supervisor, name string) *procIdentity {
	id := newProcIdentity(name, nil)
	sv.mu.Lock()
	sv.identities[name] = id
	sv.mu.Unlock()
	return id
}

// A restarted daemon reserves the keys of the sessions it is about to
// adopt before any agent launches: a new agent named after a renamed one's
// launch-time key must not take it, or the renamed agent's adoption would
// take the key back from under it. Until the adoption, the surviving mod
// is told to retry; any other unopened key is stale for good.
func TestAdoptedKeysAreReservedAheadOfFreshLaunches(t *testing.T) {
	sv, hub := adoptionSupervisor(t)
	sv.ReserveAdoptions([]string{"beta"})

	alpha := addIdentity(sv, "alpha")
	if key := sv.allocBridgeKey("alpha", alpha); key == "alpha" || !strings.HasPrefix(key, "alpha.") {
		t.Fatalf("a fresh alpha keyed %q, the key beta's session is to be adopted under", key)
	}
	if _, err := hub.Connect("alpha", "launch-old"); !errors.Is(err, bridge.ErrNotOpen) {
		t.Fatalf("the surviving mod's connect before adoption: err=%v, want ErrNotOpen (retry)", err)
	}
	if _, err := hub.Connect("gamma", "launch-x"); !errors.Is(err, bridge.ErrStaleLaunch) {
		t.Fatalf("a key nobody adopts: err=%v, want ErrStaleLaunch", err)
	}

	beta := addIdentity(sv, "beta")
	bl := sv.adoptBridge(beta, "alpha", "launch-old", "", nil)
	if !bl.bridged || beta.BridgeKey() != "alpha" {
		t.Fatalf("beta's adoption: %+v, key %q", bl, beta.BridgeKey())
	}
	if key := sv.allocBridgeKey("alpha", alpha); key == "alpha" {
		t.Fatal("a fresh alpha took the key beta's adopted session holds")
	}
}

// An agent that does not adopt (its session ended, or it is adopted
// legacy) gives its reservation back: its own fresh launch may key as its
// name, and the old session's mod is told to stop.
func TestAReleasedReservationFreesTheKey(t *testing.T) {
	sv, hub := adoptionSupervisor(t)
	sv.ReserveAdoptions([]string{"alpha"})
	alpha := addIdentity(sv, "alpha")
	if key := sv.allocBridgeKey("alpha", alpha); key != "alpha" {
		t.Fatalf("alpha's own reservation kept it from its key: %q", key)
	}
	sv.ReleaseAdoption("alpha")
	if _, err := hub.Connect("alpha", "launch-old"); !errors.Is(err, bridge.ErrStaleLaunch) {
		t.Fatalf("a released adoption's mod: err=%v, want ErrStaleLaunch", err)
	}
	other := addIdentity(sv, "zed")
	if key := sv.allocBridgeKey("alpha", other); key != "alpha" {
		t.Fatalf("a released key stayed reserved: %q", key)
	}
}

// Should a fresh launch hold the key anyway (it allocated before the
// reservation), the adoption gives way: the session is adopted legacy, the
// holder's generation stays, and its messages keep reaching it alone.
func TestAdoptionNeverTakesAKeyAnotherAgentHolds(t *testing.T) {
	sv, hub := adoptionSupervisor(t)
	alpha := addIdentity(sv, "alpha")
	own, err := hub.Open("alpha", "launch-new")
	if err != nil {
		t.Fatal(err)
	}
	alpha.setBridge(own)
	if _, err := hub.Connect("alpha", "launch-new"); err != nil {
		t.Fatal(err)
	}

	beta := addIdentity(sv, "beta")
	if bl := sv.adoptBridge(beta, "alpha", "launch-old", "", nil); bl.bridged || beta.BridgeKey() != "" {
		t.Fatalf("beta adopted the key alpha holds: %+v", bl)
	}
	if target, ok := sv.BridgeRouter().Route("alpha"); !ok || target != own {
		t.Fatalf("alpha's route = %+v, %v; want its own generation %+v", target, ok, own)
	}
	if _, ok := sv.BridgeRouter().Route("beta"); ok {
		t.Fatal("beta was routed to a bridge it does not hold")
	}
}

// A rename between the restore and the adoption carries the reservation.
func TestARenameCarriesTheReservation(t *testing.T) {
	sv, _ := adoptionSupervisor(t)
	sv.ReserveAdoptions([]string{"beta"})
	addIdentity(sv, "beta")
	sv.mu.Lock()
	sv.states["beta"] = &ProcessState{Name: "beta", Status: "running", Ephemeral: true}
	sv.mu.Unlock()
	if err := sv.RenameAgent("beta", "delta"); err != nil {
		t.Fatal(err)
	}
	alpha := addIdentity(sv, "alpha")
	if key := sv.allocBridgeKey("alpha", alpha); key == "alpha" {
		t.Fatal("the renamed agent's reservation was dropped")
	}
	sv.ReleaseAdoption("delta")
	if key := sv.allocBridgeKey("alpha", alpha); key != "alpha" {
		t.Fatalf("the reservation was not released under its new owner: %q", key)
	}
}

// An agent restored to adopt whose session is gone by the time its loop
// runs launches fresh: it gives up the reservation, keys as its own name,
// and the dead session's key is no longer awaited.
func TestAFreshLaunchGivesUpItsAdoption(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "LEO_BRIDGE_AGENT=alpha\nLEO_BRIDGE_LAUNCH=launch-old")
	spec := claudeSpec(t, "alpha")
	spec.Adopt = true
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, spec, func(o *bridgeTestOpts) { o.adoptions = []string{"alpha"} })
	waitForNewSessions(t, logPath, 1)
	waitFor(t, "the fresh launch's key", func() bool { key, ok := f.sv.BridgeKey("alpha"); return ok && key == "alpha" })
	f.sv.mu.RLock()
	left := len(f.sv.adoptionKeys)
	f.sv.mu.RUnlock()
	if left != 0 {
		t.Fatalf("%d adoption reservations outlived the fresh launch", left)
	}
}
