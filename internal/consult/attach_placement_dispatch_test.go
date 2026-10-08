package consult

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

func newAttachDispatcher(t *testing.T, queried *[]string, clients ...tmux.Client) (*Dispatcher, *AttachPlacements) {
	t.Helper()
	d := newNestedDispatcher(t)
	now := attachEpoch
	reg := newTestAttachPlacements(&now)
	d.SetAttachPlacements(reg, func(_ context.Context, session string) ([]tmux.Client, error) {
		*queried = append(*queried, session)
		return clients, nil
	})
	return d, reg
}

func TestAttachPlacementUsesTheCallersOwnSessionForARootDispatch(t *testing.T) {
	var queried []string
	d, reg := newAttachDispatcher(t, &queried, clientAt(10, attachEpoch))
	reg.Register("$1", 10, "background")
	root := startNested(t, d, Request{Caller: "alpha", CallerSessionID: "$1"})

	got := d.ApplyAttachPlacement(context.Background(), root, ViewerOverrides{}, &config.Config{})
	if got.Placement != "background" || len(queried) != 1 || queried[0] != "$1" {
		t.Fatalf("got %+v queried %v", got, queried)
	}
}

func TestAttachPlacementResolvesNestedDispatchesAgainstTheRootCallersSession(t *testing.T) {
	var queried []string
	d, reg := newAttachDispatcher(t, &queried, clientAt(10, attachEpoch.Add(time.Second)))
	reg.Register("$1", 10, "background")
	root := startNested(t, d, Request{Caller: "alpha", CallerSessionID: "$1"})
	// Each nested requester reports a session of its own (its dispatch pane).
	child := startNested(t, d, Request{ParentDispatchID: root.ID, CallerSessionID: "$7"})
	grandchild := startNested(t, d, Request{ParentDispatchID: child.ID, CallerSessionID: "$8"})

	for name, rec := range map[string]Record{"child": child, "grandchild": grandchild} {
		queried = nil
		got := d.ApplyAttachPlacement(context.Background(), rec, ViewerOverrides{}, &config.Config{})
		if got.Placement != "background" || len(queried) != 1 || queried[0] != "$1" {
			t.Fatalf("%s: got %+v queried %v, want background resolved against $1", name, got, queried)
		}
	}
	if child.Caller != "alpha" {
		t.Fatalf("child caller=%q; the record keeps its root-caller semantics", child.Caller)
	}
}

func TestAttachPlacementWithoutRegistryLeavesOverridesAlone(t *testing.T) {
	d := newNestedDispatcher(t)
	base := ViewerOverrides{Placement: "window"}
	if got := d.ApplyAttachPlacement(context.Background(), Record{CallerSessionID: "$1"}, base, &config.Config{}); got != base {
		t.Fatalf("got %+v", got)
	}
}

func TestInteractiveDispatchFollowsTheAttachedClientsPlacement(t *testing.T) {
	cfg := testConfig()
	var queried []string
	d, reg := newAttachDispatcher(t, &queried, clientAt(10, attachEpoch))
	rt := &fakeInteractiveRuntime{}
	d.SetInteractiveRuntime(rt)
	req := Request{Template: "claude", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive, Kind: "dispatch", CallerPaneID: "%1", CallerSessionID: "$1", CallerWindowID: "@1"}

	// An unregistered client contributes the default (pane), so a caller pane
	// gets a split.
	if _, err := d.Start(context.Background(), cfg, req); err != nil {
		t.Fatalf("Start: %v", err)
	}
	reg.Register("$1", 10, "background")
	if _, err := d.Start(context.Background(), cfg, req); err != nil {
		t.Fatalf("Start: %v", err)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.placements) != 2 {
		t.Fatalf("placements=%v", rt.placements)
	}
	if rt.placements[len(rt.placements)-1] != "window" {
		t.Fatalf("placements=%v, want the registered background window last", rt.placements)
	}
	if rt.placements[0] != "split" {
		t.Fatalf("placements=%v, want the unflagged default to split first", rt.placements)
	}
}

func TestViewerOnStartAppliesPlacementOverrides(t *testing.T) {
	var calls [][]string
	v := &Viewer{ConfigPath: viewerConfig(t, "pane"), TmuxPath: "tmux", Executable: func() (string, error) { return "/opt/leo", nil }, ResolveCaller: func(string) (string, bool) { return "leo-caller", true }, ExecCommand: func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		if len(args) > 2 && args[2] == "new-window" {
			return exec.Command("printf", "@7\\n")
		}
		return exec.Command("true")
	}}
	v.PlacementOverrides = func(_ context.Context, rec Record, base ViewerOverrides, _ *config.Config) ViewerOverrides {
		base.Placement = "background"
		return base
	}
	rec := Record{ID: "d-123abc", Kind: "dispatch", Caller: "caller", Template: "claude", CallerPaneID: "%1", CallerSessionID: "$1", CallerWindowID: "@1"}
	if got := v.OnStart(rec); got != "@7" {
		t.Fatalf("OnStart=%q", got)
	}
	for _, c := range calls {
		if containsArg(c, "=leo-caller") {
			t.Fatalf("touched the caller session: %#v", c)
		}
	}
}
