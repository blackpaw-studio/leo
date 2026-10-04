package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

type taskRouteState struct {
	key     string
	planned bool
}

type injectRig struct {
	hub      *bridge.Hub
	pasteErr error
	injector func(ctx context.Context, session, prompt string) error

	mu     sync.Mutex
	pasted []string
	routes map[string]taskRouteState
}

// testTaskSettle bounds the rig's wait for a launch to settle on the bridge.
const testTaskSettle = 50 * time.Millisecond

func newInjectRig(t *testing.T) *injectRig {
	t.Helper()
	return newInjectRigSettling(t, testTaskSettle)
}

func newInjectRigSettling(t *testing.T, settle time.Duration) *injectRig {
	t.Helper()
	g := &injectRig{hub: bridge.New(bridge.Options{}), routes: map[string]taskRouteState{
		"leo-alpha": {key: "alpha", planned: true},
		"leo-beta":  {planned: true},
	}}
	t.Cleanup(g.hub.Close)
	route := func(session string) (string, bool) {
		g.mu.Lock()
		defer g.mu.Unlock()
		r, ok := g.routes[session]
		if !ok {
			return "", true
		}
		return r.key, r.planned
	}
	paste := func(_ context.Context, session, prompt string) error {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.pasted = append(g.pasted, session+"|"+prompt)
		return g.pasteErr
	}
	inject := taskInjector(g.hub, route, settle, paste)
	g.injector = func(ctx context.Context, session, prompt string) error {
		res, err := inject(ctx, session, prompt)
		if res != nil {
			t.Fatalf("injector returned a result %+v; delivery is fire-and-forget", res)
		}
		return err
	}
	return g
}

func (g *injectRig) setRoute(session string, r taskRouteState) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.routes[session] = r
}

func (g *injectRig) pastes() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.pasted...)
}

// A persistent task's prompt reaches a bridged agent as the user's own
// prompt, verbatim, and the injection returns once the mod accepts it.
func TestTaskInjectorDeliversOverALiveBridge(t *testing.T) {
	g := newInjectRig(t)
	stream, err := g.hub.Connect("alpha")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- g.injector(context.Background(), "leo-alpha", "run the nightly report\nnow") }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd, err := stream.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Op != bridge.OpDeliver || !cmd.AsUser || cmd.Text != "run the nightly report\nnow" {
		t.Fatalf("deliver = %+v", cmd)
	}
	if err := g.hub.Apply("alpha", bridge.Report{Type: bridge.ReportAck, ID: cmd.ID, OK: true}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("inject = %v", err)
	}
	if p := g.pastes(); len(p) != 0 {
		t.Fatalf("bridged task was also pasted: %q", p)
	}
}

// A bridge that does not ack within the invocation's deadline is an error;
// the prompt is never pasted as well (it stays queued on the bridge).
func TestTaskInjectorBridgeTimeoutIsAnErrorNotAPaste(t *testing.T) {
	g := newInjectRig(t)
	if _, err := g.hub.Connect("alpha"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := g.injector(ctx, "leo-alpha", "task"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("inject = %v, want the deadline", err)
	}
	if len(g.pastes()) != 0 {
		t.Fatal("a timed-out bridge deliver fell back to tmux")
	}
}

func TestTaskInjectorPastesWithoutALiveBridge(t *testing.T) {
	for _, tc := range []struct{ name, session string }{
		{"bridge launched but not connected", "leo-alpha"},
		{"no bridge", "leo-beta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newInjectRig(t)
			if err := g.injector(context.Background(), tc.session, "task"); err != nil {
				t.Fatal(err)
			}
			if p := g.pastes(); len(p) != 1 || p[0] != tc.session+"|task" {
				t.Fatalf("pasted = %q", p)
			}
		})
	}
}

func TestTaskInjectorWithoutAHubPastes(t *testing.T) {
	var pasted int
	inject := taskInjector(nil, nil, time.Minute, func(context.Context, string, string) error { pasted++; return nil })
	if _, err := inject(context.Background(), "leo-alpha", "task"); err != nil || pasted != 1 {
		t.Fatalf("inject = %v, pasted %d", err, pasted)
	}
}

// A task for an agent the ensure step just spawned arrives before the
// agent's launch is planned and before its mod connects; it waits for the
// bridge rather than racing into a paste.
func TestTaskInjectorWaitsForAJustLaunchedBridge(t *testing.T) {
	g := newInjectRigSettling(t, time.Minute)
	g.setRoute("leo-alpha", taskRouteState{})
	done := make(chan error, 1)
	go func() { done <- g.injector(context.Background(), "leo-alpha", "task") }()
	time.Sleep(20 * time.Millisecond)
	g.setRoute("leo-alpha", taskRouteState{key: "alpha", planned: true})
	time.Sleep(20 * time.Millisecond)
	stream, err := g.hub.Connect("alpha")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd, err := stream.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Op != bridge.OpDeliver || cmd.Text != "task" {
		t.Fatalf("deliver = %+v", cmd)
	}
	if err := g.hub.Apply("alpha", bridge.Report{Type: bridge.ReportAck, ID: cmd.ID, OK: true}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if p := g.pastes(); len(p) != 0 {
		t.Fatalf("pasted %q while the bridge was coming up", p)
	}
}

// A launch planned without the bridge is pasted into at once.
func TestTaskInjectorPastesALegacyLaunchWithoutWaiting(t *testing.T) {
	g := newInjectRig(t)
	start := time.Now()
	if err := g.injector(context.Background(), "leo-beta", "task"); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited >= testTaskSettle {
		t.Fatalf("waited %s for a legacy launch", waited)
	}
	if p := g.pastes(); len(p) != 1 {
		t.Fatalf("pasted = %q", p)
	}
}

// A launch that never settles is pasted into once the wait is over.
func TestTaskInjectorPastesWhenTheLaunchNeverSettles(t *testing.T) {
	g := newInjectRig(t)
	g.setRoute("leo-alpha", taskRouteState{})
	if err := g.injector(context.Background(), "leo-alpha", "task"); err != nil {
		t.Fatal(err)
	}
	if p := g.pastes(); len(p) != 1 {
		t.Fatalf("pasted = %q", p)
	}
}

// The invocation's own deadline ends the wait without pasting: the
// invocation has failed.
func TestTaskInjectorWaitEndsWithTheInvocation(t *testing.T) {
	g := newInjectRig(t)
	g.setRoute("leo-alpha", taskRouteState{})
	ctx, cancel := context.WithTimeout(context.Background(), testTaskSettle/5)
	defer cancel()
	if err := g.injector(ctx, "leo-alpha", "task"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("inject = %v, want the invocation deadline", err)
	}
	if p := g.pastes(); len(p) != 0 {
		t.Fatalf("pasted %q after the invocation ended", p)
	}
}
