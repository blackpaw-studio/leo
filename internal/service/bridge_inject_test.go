package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

type injectRig struct {
	hub      *bridge.Hub
	pasted   []string
	pasteErr error
	keys     map[string]string
	injector func(ctx context.Context, session, prompt string) error
}

func newInjectRig(t *testing.T) *injectRig {
	t.Helper()
	g := &injectRig{hub: bridge.New(bridge.Options{}), keys: map[string]string{"leo-alpha": "alpha"}}
	t.Cleanup(g.hub.Close)
	route := func(session string) (string, bool) { k, ok := g.keys[session]; return k, ok }
	paste := func(_ context.Context, session, prompt string) error {
		g.pasted = append(g.pasted, session+"|"+prompt)
		return g.pasteErr
	}
	inject := taskInjector(g.hub, route, paste)
	g.injector = func(ctx context.Context, session, prompt string) error {
		res, err := inject(ctx, session, prompt)
		if res != nil {
			t.Fatalf("injector returned a result %+v; delivery is fire-and-forget", res)
		}
		return err
	}
	return g
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
	if len(g.pasted) != 0 {
		t.Fatalf("bridged task was also pasted: %q", g.pasted)
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
	if len(g.pasted) != 0 {
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
			if len(g.pasted) != 1 || g.pasted[0] != tc.session+"|task" {
				t.Fatalf("pasted = %q", g.pasted)
			}
		})
	}
}

func TestTaskInjectorWithoutAHubPastes(t *testing.T) {
	var pasted int
	inject := taskInjector(nil, nil, func(context.Context, string, string) error { pasted++; return nil })
	if _, err := inject(context.Background(), "leo-alpha", "task"); err != nil || pasted != 1 {
		t.Fatalf("inject = %v, pasted %d", err, pasted)
	}
}
