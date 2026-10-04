package service

import (
	"context"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

func TestShouldSuspend(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	idle := 30 * time.Minute

	cases := []struct {
		name string
		act  tmux.SessionActivity
		idle time.Duration
		want bool
	}{
		{"idle past threshold, detached", tmux.SessionActivity{Attached: 0, LastActivity: now.Add(-31 * time.Minute)}, idle, true},
		{"idle under threshold", tmux.SessionActivity{Attached: 0, LastActivity: now.Add(-29 * time.Minute)}, idle, false},
		{"attached blocks suspend", tmux.SessionActivity{Attached: 1, LastActivity: now.Add(-2 * time.Hour)}, idle, false},
		{"disabled interval", tmux.SessionActivity{Attached: 0, LastActivity: now.Add(-2 * time.Hour)}, 0, false},
		{"exactly at threshold suspends", tmux.SessionActivity{Attached: 0, LastActivity: now.Add(-30 * time.Minute)}, idle, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldSuspend(now, c.act, nil, c.idle); got != c.want {
				t.Fatalf("shouldSuspend = %v, want %v", got, c.want)
			}
		})
	}
}

// With a connected bridge, idleness is the mod's word, not the pane's: a
// claude TUI redraws (spinners, status line) without doing anything, and
// sits silent through a long tool call that is very much work.
func TestShouldSuspendWithAConnectedBridge(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	idle := 30 * time.Minute
	quietPane := tmux.SessionActivity{LastActivity: now.Add(-2 * time.Hour)}
	busyPane := tmux.SessionActivity{LastActivity: now}
	cases := []struct {
		name string
		act  tmux.SessionActivity
		st   bridge.State
		want bool
	}{
		{"idle since the last turn, though the pane redraws", busyPane, bridge.State{Connected: true, LastTurnComplete: now.Add(-31 * time.Minute)}, true},
		{"last turn too recent, though the pane is quiet", quietPane, bridge.State{Connected: true, LastTurnComplete: now.Add(-29 * time.Minute)}, false},
		{"a running turn is never idle", quietPane, bridge.State{Connected: true, Busy: true, LastTurnComplete: now.Add(-2 * time.Hour)}, false},
		{"no turn yet: idle since the mod connected", busyPane, bridge.State{Connected: true, ConnectedAt: now.Add(-31 * time.Minute)}, true},
		{"no turn yet, connected recently", quietPane, bridge.State{Connected: true, ConnectedAt: now.Add(-time.Minute)}, false},
		{"an attached client still blocks suspend", tmux.SessionActivity{Attached: 1}, bridge.State{Connected: true, LastTurnComplete: now.Add(-2 * time.Hour)}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := c.st
			if got := shouldSuspend(now, c.act, &st, idle); got != c.want {
				t.Fatalf("shouldSuspend = %v, want %v", got, c.want)
			}
		})
	}
}

func TestSupervisorBridgeStateOnlyWhenConnected(t *testing.T) {
	sv := NewSupervisor(context.Background())
	hub := bridge.New(bridge.Options{})
	t.Cleanup(hub.Close)
	sv.SetBridge(hub, nil, 0)
	id := newProcIdentity("alpha", nil)
	id.setBridgeKey("alpha")
	sv.mu.Lock()
	sv.identities["alpha"] = id
	sv.mu.Unlock()
	if _, ok := sv.BridgeState("alpha"); ok {
		t.Fatal("BridgeState before the mod connected")
	}
	if _, err := hub.Connect("alpha"); err != nil {
		t.Fatal(err)
	}
	if st, ok := sv.BridgeState("alpha"); !ok || !st.Connected {
		t.Fatalf("BridgeState = %+v, %v", st, ok)
	}
	if _, ok := sv.BridgeState("beta"); ok {
		t.Fatal("BridgeState for an unknown agent")
	}
}

func TestParseIdle(t *testing.T) {
	if parseIdle("") != 0 || parseIdle("bad") != 0 || parseIdle("-5m") != 0 {
		t.Fatal("invalid/empty/negative durations must parse to 0")
	}
	if parseIdle("24h") != 24*time.Hour {
		t.Fatal("24h should parse")
	}
}

func TestSupervisorBridgeStatus(t *testing.T) {
	sv := NewSupervisor(context.Background())
	hub := bridge.New(bridge.Options{})
	t.Cleanup(hub.Close)
	sv.SetBridge(hub, nil, 0)
	add := func(name, harnessName, key string) {
		id := newProcIdentity(name, nil)
		id.harness = harnessName
		id.setBridgeKey(key)
		sv.mu.Lock()
		sv.identities[name] = id
		sv.mu.Unlock()
	}
	add("bridged", "claude", "bridged")
	add("legacy", "claude", "")
	add("implicit", "", "")
	add("codex", "codex", "")
	if _, err := hub.Connect("bridged"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"bridged": "connected", "legacy": "absent", "implicit": "absent", "codex": "", "unknown": "",
	} {
		if got := sv.BridgeStatus(name); got != want {
			t.Errorf("BridgeStatus(%s) = %q, want %q", name, got, want)
		}
	}
	hub.Forget("bridged")
	if got := sv.BridgeStatus("bridged"); got != "absent" {
		t.Errorf("BridgeStatus after forget = %q, want absent", got)
	}
}
