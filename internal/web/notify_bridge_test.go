package web

import (
	"context"
	"os/exec"
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// The caller's primary pane is read from its agent session's
// @leo_primary_pane option, the pane its claude runs in.
func TestAgentPrimaryPaneReadsTheSessionOption(t *testing.T) {
	var argv []string
	s := &Server{execCommandContext: func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		argv = args
		return exec.CommandContext(ctx, "printf", "%%4\n")
	}}
	pane, err := s.agentPrimaryPane(context.Background(), "orch")
	if err != nil || pane != "%4" {
		t.Fatalf("agentPrimaryPane = %q, %v; want %%4", pane, err)
	}
	want := tmux.Args("show-options", "-t", "=leo-orch:", "-v", "@leo_primary_pane")
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv = %q, want %q", argv, want)
	}
}

func TestAgentPrimaryPaneFailsWithoutAPane(t *testing.T) {
	s := &Server{execCommandContext: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "printf", "\n")
	}}
	if pane, err := s.agentPrimaryPane(context.Background(), "orch"); err == nil {
		t.Fatalf("agentPrimaryPane = %q, want an error for an empty option", pane)
	}
}

// With a bridge router, notifications try the caller's bridge first; with
// none, the tmux/peer-inbox delivery is used as before.
func TestNotificationDeliveryPrefersTheBridge(t *testing.T) {
	s := &Server{execCommandContext: func(ctx context.Context, _ string, _ ...string) *exec.Cmd { return exec.CommandContext(ctx, "true") }}
	if _, ok := s.notificationDelivery(nil).(*consult.TmuxNotificationDelivery); !ok {
		t.Fatal("without a bridge router the delivery is not the tmux one")
	}
	router := &bridge.Router{Hub: bridge.New(bridge.Options{})}
	t.Cleanup(router.Hub.Close)
	got, ok := s.notificationDelivery(router).(*consult.BridgeNotificationDelivery)
	if !ok {
		t.Fatal("with a bridge router the delivery is not the bridge one")
	}
	if _, ok := got.Fallback.(*consult.TmuxNotificationDelivery); !ok {
		t.Fatal("the bridge delivery does not fall back to the tmux one")
	}
}

// A bridge key's owner is the agent holding it now: a renamed agent keeps
// the key its claude launched under.
func TestBridgeKeyOwnerFindsTheAgentHoldingTheKey(t *testing.T) {
	keys := map[string]bridge.Target{"orch-renamed": {Key: "orch", Gen: 1}, "other": {Key: "other", Gen: 2}}
	s := &Server{
		processes:    &mockProcesses{states: map[string]ProcessStateInfo{"orch-renamed": {}, "other": {}}},
		bridgeRouter: &bridge.Router{Targets: func(name string) (bridge.Target, bool) { t, ok := keys[name]; return t, ok }},
	}
	if owner, ok := s.bridgeKeyOwner("orch"); !ok || owner != "orch-renamed" {
		t.Fatalf("bridgeKeyOwner(orch) = %q, %v; want orch-renamed", owner, ok)
	}
	if owner, ok := s.bridgeKeyOwner("nobody"); ok {
		t.Fatalf("bridgeKeyOwner(nobody) = %q, want none", owner)
	}
	if _, ok := (&Server{}).bridgeKeyOwner("orch"); ok {
		t.Fatal("a server without processes or router found an owner")
	}
}
