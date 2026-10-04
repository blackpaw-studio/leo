package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/harness"
)

type injectRig struct {
	hub      *bridge.Hub
	alpha    bridge.Target // the generation leo-alpha's launch opened
	pasteErr error
	injector func(ctx context.Context, session, prompt string) error
	// inject is the injector itself, result included.
	inject func(ctx context.Context, session, prompt string) (*harness.Result, error)

	mu     sync.Mutex
	pasted []string
	routes map[string]BridgeRoute
	// queued records the durable queue's calls ("<session>"); nil when the
	// rig queues in the hub alone.
	queued []string
}

// testTaskSettle bounds the rig's wait for a launch to settle on the bridge.
const testTaskSettle = 50 * time.Millisecond

// rigLaunch is the launch the rig opens alpha's key for.
const rigLaunch = "launch-1"

func newInjectRig(t *testing.T) *injectRig {
	t.Helper()
	return newInjectRigSettling(t, testTaskSettle)
}

func newInjectRigSettling(t *testing.T, settle time.Duration) *injectRig {
	t.Helper()
	return newInjectRigWith(t, settle, false)
}

// newDurableInjectRig is newInjectRig queueing delivers durably, as the
// daemon does (see Supervisor.QueueDeliverForSession).
func newDurableInjectRig(t *testing.T) *injectRig {
	t.Helper()
	return newInjectRigWith(t, testTaskSettle, true)
}

func newInjectRigWith(t *testing.T, settle time.Duration, isDurable bool) *injectRig {
	t.Helper()
	hub := bridge.New(bridge.Options{})
	t.Cleanup(hub.Close)
	alpha, err := hub.Open("alpha", rigLaunch)
	if err != nil {
		t.Fatal(err)
	}
	g := &injectRig{hub: hub, alpha: alpha, routes: map[string]BridgeRoute{
		"leo-alpha": {Target: alpha, Planned: true},
		"leo-beta":  {Planned: true},
	}}
	route := func(session string) BridgeRoute {
		g.mu.Lock()
		defer g.mu.Unlock()
		r, ok := g.routes[session]
		if !ok {
			return BridgeRoute{Planned: true}
		}
		return r
	}
	paste := func(_ context.Context, session, prompt string) error {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.pasted = append(g.pasted, session+"|"+prompt)
		return g.pasteErr
	}
	var queue func(string, bridge.Target, bridge.Command) (*bridge.Ticket, error)
	if isDurable {
		queue = func(session string, target bridge.Target, cmd bridge.Command) (*bridge.Ticket, error) {
			g.mu.Lock()
			g.queued = append(g.queued, session)
			g.mu.Unlock()
			return g.hub.EnqueueTo(target, cmd)
		}
	}
	inject := taskInjector(g.hub, route, settle, paste, queue)
	g.inject = inject
	g.injector = func(ctx context.Context, session, prompt string) error {
		res, err := inject(ctx, session, prompt)
		if res != nil {
			t.Fatalf("injector returned a result %+v; delivery is fire-and-forget", res)
		}
		return err
	}
	return g
}

func (g *injectRig) setRoute(session string, r BridgeRoute) {
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
	stream, err := g.hub.Connect("alpha", rigLaunch)
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
	if err := g.hub.Apply("alpha", rigLaunch, bridge.Report{Type: bridge.ReportAck, ID: cmd.ID, OK: true}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("inject = %v", err)
	}
	if p := g.pastes(); len(p) != 0 {
		t.Fatalf("bridged task was also pasted: %q", p)
	}
}

// An agent busy past the invocation's deadline has not failed the task: the
// deliver stays queued and runs when the turn ends. The invocation completes
// as queued — not failed, and not left to time out, which would interrupt
// the agent's unrelated running turn — and the prompt is never pasted too.
func TestTaskInjectorBusyPastTheDeadlineIsQueuedNotFailed(t *testing.T) {
	g := newInjectRig(t)
	if _, err := g.hub.Connect("alpha", rigLaunch); err != nil {
		t.Fatal(err)
	}
	if err := g.hub.Apply("alpha", rigLaunch, bridge.Report{Type: bridge.ReportEvent, Name: bridge.EventTurnStart}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	res, err := g.inject(ctx, "leo-alpha", "task")
	if err != nil {
		t.Fatalf("inject = %v, want the task reported queued", err)
	}
	if res == nil || res.IsError || !strings.Contains(res.Text, "queued") {
		t.Fatalf("result = %+v, want a successful queued result", res)
	}
	if got := g.hub.State("alpha").Pending; got != 1 {
		t.Fatalf("pending=%d, want the deliver still queued", got)
	}
	if len(g.pastes()) != 0 {
		t.Fatal("a queued bridge deliver fell back to tmux")
	}
}

// A refusal or a lost bridge is a real failure.
func TestTaskInjectorBridgeRejectionIsAnError(t *testing.T) {
	g := newInjectRig(t)
	stream, err := g.hub.Connect("alpha", rigLaunch)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- g.injector(context.Background(), "leo-alpha", "task") }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd, err := stream.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.hub.Apply("alpha", rigLaunch, bridge.Report{Type: bridge.ReportAck, ID: cmd.ID, OK: false, Error: "dropped"}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, bridge.ErrRejected) {
		t.Fatalf("inject = %v, want the rejection", err)
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
	inject := taskInjector(nil, nil, time.Minute, func(context.Context, string, string) error { pasted++; return nil }, nil)
	if _, err := inject(context.Background(), "leo-alpha", "task"); err != nil || pasted != 1 {
		t.Fatalf("inject = %v, pasted %d", err, pasted)
	}
}

// A task for an agent the ensure step just spawned arrives before the
// agent's launch is planned and before its mod connects; it waits for the
// bridge rather than racing into a paste.
func TestTaskInjectorWaitsForAJustLaunchedBridge(t *testing.T) {
	g := newInjectRigSettling(t, time.Minute)
	g.setRoute("leo-alpha", BridgeRoute{})
	done := make(chan error, 1)
	go func() { done <- g.injector(context.Background(), "leo-alpha", "task") }()
	time.Sleep(20 * time.Millisecond)
	g.setRoute("leo-alpha", BridgeRoute{Target: g.alpha, Planned: true})
	time.Sleep(20 * time.Millisecond)
	stream, err := g.hub.Connect("alpha", rigLaunch)
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
	if err := g.hub.Apply("alpha", rigLaunch, bridge.Report{Type: bridge.ReportAck, ID: cmd.ID, OK: true}); err != nil {
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
	g.setRoute("leo-alpha", BridgeRoute{})
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
	g.setRoute("leo-alpha", BridgeRoute{})
	ctx, cancel := context.WithTimeout(context.Background(), testTaskSettle/5)
	defer cancel()
	if err := g.injector(ctx, "leo-alpha", "task"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("inject = %v, want the invocation deadline", err)
	}
	if p := g.pastes(); len(p) != 0 {
		t.Fatalf("pasted %q after the invocation ended", p)
	}
}

// A loaded machine can leave the wait unscheduled until both the
// invocation's deadline and the settle deadline have passed. The
// invocation's end still wins: nothing is pasted into an agent for a call
// that already ended. route sleeps (fake time) past both deadlines so both
// are ready when the wait next looks; the scenario repeats because a wait
// that picked between them at random would pass by luck half the time.
func TestTaskInjectorInvocationEndBeatsASimultaneousSettle(t *testing.T) {
	const settle = time.Minute
	for i := range 64 {
		synctest.Test(t, func(t *testing.T) {
			hub := bridge.New(bridge.Options{})
			defer hub.Close()
			route := func(string) BridgeRoute {
				time.Sleep(2 * settle)
				return BridgeRoute{}
			}
			pasted := 0
			paste := func(context.Context, string, string) error { pasted++; return nil }
			inject := taskInjector(hub, route, settle, paste, nil)
			ctx, cancel := context.WithTimeout(context.Background(), settle/2)
			defer cancel()
			if _, err := inject(ctx, "leo-alpha", "task"); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("run %d: inject = %v, want the invocation deadline", i, err)
			}
			if pasted != 0 {
				t.Fatalf("run %d: pasted after the invocation ended", i)
			}
		})
	}
}

// A launch has settled on the bridge once its mod connected since it
// started, even if the stream is down for a reconnect right now: the task
// rides the bridge (queued until the mod is back), never a paste.
func TestTaskInjectorDeliversToAModBetweenReconnects(t *testing.T) {
	g := newInjectRig(t)
	g.setRoute("leo-alpha", BridgeRoute{Target: g.alpha, Planned: true})
	first, err := g.hub.Connect("alpha", rigLaunch)
	if err != nil {
		t.Fatal(err)
	}
	first.Close()
	done := make(chan error, 1)
	go func() { done <- g.injector(context.Background(), "leo-alpha", "task") }()
	if _, err := g.hub.WaitFor(context.Background(), "alpha", func(st bridge.State) bool { return st.Pending == 1 }); err != nil {
		t.Fatal(err)
	}
	stream, err := g.hub.Connect("alpha", rigLaunch)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd, err := stream.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.hub.Apply("alpha", rigLaunch, bridge.Report{Type: bridge.ReportAck, ID: cmd.ID, OK: true}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if p := g.pastes(); len(p) != 0 {
		t.Fatalf("pasted %q to a bridged launch", p)
	}
}

// A predecessor launch's mod cannot settle its successor's launch: it is
// refused, and the successor's launch, whose mod never connected, is pasted
// into once the wait is over.
func TestTaskInjectorIgnoresAPredecessorsMod(t *testing.T) {
	g := newInjectRig(t)
	successor, err := g.hub.Open("alpha", "launch-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.hub.Connect("alpha", rigLaunch); !errors.Is(err, bridge.ErrStaleLaunch) {
		t.Fatalf("the predecessor connected: err=%v, want ErrStaleLaunch", err)
	}
	g.setRoute("leo-alpha", BridgeRoute{Target: successor, Planned: true})
	if err := g.injector(context.Background(), "leo-alpha", "task"); err != nil {
		t.Fatal(err)
	}
	if p := g.pastes(); len(p) != 1 {
		t.Fatalf("pasted = %q; want a paste once the launch never settled", p)
	}
}

// A durable task prompt whose launch ends before the mod takes it waits in
// the agent's outbox for its next launch: the invocation completes as
// queued, not failed (a retry would run it twice), and is never pasted.
func TestTaskInjectorDurablePromptOutlivesItsLaunch(t *testing.T) {
	g := newDurableInjectRig(t)
	stream, err := g.hub.Connect("alpha", rigLaunch)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		res *harness.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := g.inject(context.Background(), "leo-alpha", "task")
		done <- outcome{res, err}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := stream.Next(ctx); err != nil {
		t.Fatal(err)
	}
	g.hub.Forget("alpha")
	got := <-done
	if got.err != nil || got.res == nil || !strings.Contains(got.res.Text, "queued") {
		t.Fatalf("inject = %+v, %v; want a queued result", got.res, got.err)
	}
	g.mu.Lock()
	queued := append([]string(nil), g.queued...)
	g.mu.Unlock()
	if len(queued) != 1 || queued[0] != "leo-alpha" {
		t.Fatalf("queued durably %q, want once for leo-alpha", queued)
	}
	if len(g.pastes()) != 0 {
		t.Fatal("a kept task prompt was also pasted")
	}
}
