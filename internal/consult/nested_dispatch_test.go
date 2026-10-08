package consult

import (
	"context"
	"os/exec"
	"testing"
)

// startNested starts a headless dispatch that stays running, so its record is
// live in the store for a child to name as its parent.
func startNested(t *testing.T, d *Dispatcher, req Request) Record {
	t.Helper()
	req.Template, req.Prompt, req.Kind = "claude", "q", "dispatch"
	req.Cwd = t.TempDir()
	started, err := d.Start(context.Background(), testConfig(), req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	rec, err := d.Get(started.ID)
	if err != nil {
		t.Fatalf("Get(%s): %v", started.ID, err)
	}
	return rec
}

func newNestedDispatcher(t *testing.T) *Dispatcher {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	d := NewDispatcher(nil)
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "30")
	}
	return d
}

func TestNestedDispatchRecordsItsParentAndTheRootCaller(t *testing.T) {
	d := newNestedDispatcher(t)
	root := startNested(t, d, Request{Caller: "alpha"})
	// A subagent's own MCP server reports whatever LEO_PROCESS_NAME it
	// inherited, which need not be the root agent.
	child := startNested(t, d, Request{Caller: "inherited", ParentDispatchID: root.ID})
	grandchild := startNested(t, d, Request{Caller: "", ParentDispatchID: child.ID})

	if root.ParentDispatchID != "" {
		t.Fatalf("root parent=%q", root.ParentDispatchID)
	}
	if child.ParentDispatchID != root.ID || child.Caller != "alpha" {
		t.Fatalf("child parent=%q caller=%q, want %q/alpha", child.ParentDispatchID, child.Caller, root.ID)
	}
	if grandchild.ParentDispatchID != child.ID || grandchild.Caller != "alpha" {
		t.Fatalf("grandchild parent=%q caller=%q, want the immediate parent %q and root caller alpha", grandchild.ParentDispatchID, grandchild.Caller, child.ID)
	}
}

func TestNestedDispatchKeepsItsCallerWhenTheParentHasNone(t *testing.T) {
	d := newNestedDispatcher(t)
	parent := startNested(t, d, Request{})
	child := startNested(t, d, Request{Caller: "inherited", ParentDispatchID: parent.ID})
	if child.ParentDispatchID != parent.ID || child.Caller != "inherited" {
		t.Fatalf("parent=%q caller=%q", child.ParentDispatchID, child.Caller)
	}
}

func TestNestedDispatchIgnoresAnUnknownParent(t *testing.T) {
	d := newNestedDispatcher(t)
	for _, id := range []string{"d-000000000000", "../x", "a/b"} {
		child := startNested(t, d, Request{Caller: "alpha", ParentDispatchID: id})
		if child.ParentDispatchID != "" || child.Caller != "alpha" {
			t.Fatalf("id %q: parent=%q caller=%q; an unverifiable parent must not be recorded", id, child.ParentDispatchID, child.Caller)
		}
	}
}

func TestObservedParentPrefersTheRecordedParentAndFallsBackToTheBridgeKey(t *testing.T) {
	explicit := Record{ID: "d-c", Kind: "dispatch", ParentDispatchID: "d-p", CallerBridgeKey: DispatchBridgeKey("d-other"), StartedAt: stateNow}
	if got := observedDispatch(explicit, stateNow).ParentDispatchID; got != "d-p" {
		t.Fatalf("explicit parent=%q", got)
	}
	legacy := Record{ID: "d-c", Kind: "dispatch", CallerBridgeKey: DispatchBridgeKey("d-p"), StartedAt: stateNow}
	if got := observedDispatch(legacy, stateNow).ParentDispatchID; got != "d-p" {
		t.Fatalf("bridge-key fallback parent=%q", got)
	}
	headlessChild := Record{ID: "d-c", Kind: "dispatch", ParentDispatchID: "d-p", StartedAt: stateNow}
	if got := observedDispatch(headlessChild, stateNow).ParentDispatchID; got != "d-p" {
		t.Fatalf("headless-parent child parent=%q; it has no bridge key to derive one from", got)
	}
}

func TestANestedDispatchNeverHoldsTheAgentAboveItsParent(t *testing.T) {
	child := agentDispatch("d-c", "alpha", StatusRunning)
	child.ParentDispatchID = "d-p"
	if outstanding(child) {
		t.Fatal("a child dispatch counted toward the root agent's outstanding dispatches")
	}
	h := newObserveHarness(child)
	if got := h.obs.OutstandingDispatches(); len(got) != 0 {
		t.Fatalf("outstanding=%v", got)
	}
}
