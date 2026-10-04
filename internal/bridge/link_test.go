package bridge

import (
	"context"
	"errors"
	"testing"
)

// Subscribers that only exist once the hub is serving (the dispatch adapter
// is built with the web server) can join late and see every later event.
func TestAddSubscriberSeesLaterEvents(t *testing.T) {
	early := &recorder{}
	h := newTestHub(newFakeClock(), func(o *Options) { o.Subscriber = early })
	apply(t, h, agentA, hello("s-1"))
	late := &recorder{}
	h.AddSubscriber(late)
	apply(t, h, agentA, event(EventTurnStart))

	if got := late.snapshot(); len(got) != 1 || got[0].Name != EventTurnStart {
		t.Fatalf("late subscriber saw %+v, want only the turn.start after it joined", got)
	}
	if got := early.snapshot(); len(got) != 2 {
		t.Fatalf("construction-time subscriber saw %d events, want 2", len(got))
	}
}

func TestAddSubscriberIgnoresNil(t *testing.T) {
	h := newTestHub(newFakeClock())
	h.AddSubscriber(nil)
	apply(t, h, agentA, event(EventTurnStart)) // must not panic
}

// The opening-prompt fallback relaunches an agent without the mod only if
// its bridge never came up; the check and the forget are one step, so a
// bridge that connects at the last moment is never torn down.
func TestForgetUnlessConnected(t *testing.T) {
	t.Run("absent bridge is forgotten", func(t *testing.T) {
		h := newTestHub(newFakeClock())
		done := sendAsync(context.Background(), h, agentA, Deliver("the opening", true))
		if _, err := h.WaitFor(testCtx(t), agentA, func(s State) bool { return s.Pending == 1 }); err != nil {
			t.Fatalf("WaitFor: %v", err)
		}
		if !h.ForgetUnlessConnected(agentA) {
			t.Fatal("ForgetUnlessConnected = false for an agent that never connected")
		}
		if err := result(t, done); !errors.Is(err, ErrForgotten) {
			t.Fatalf("Send err=%v, want ErrForgotten", err)
		}
		if st := h.State(agentA); st.Pending != 0 {
			t.Fatalf("pending=%d after forget, want 0", st.Pending)
		}
	})
	t.Run("connected bridge is kept", func(t *testing.T) {
		h := newTestHub(newFakeClock())
		_ = mustConnect(t, h, agentA)
		if _, err := h.Enqueue(agentA, Deliver("the opening", true)); err != nil {
			t.Fatal(err)
		}
		if h.ForgetUnlessConnected(agentA) {
			t.Fatal("ForgetUnlessConnected = true for a connected agent")
		}
		if st := h.State(agentA); !st.Connected || st.Pending != 1 {
			t.Fatalf("connected agent's state was touched: %+v", st)
		}
	})
}

func TestRouterRoutesOnlyKnownConnectedAgents(t *testing.T) {
	h := newTestHub(newFakeClock())
	targets := map[string]Target{
		"leo-alpha":   mustOpen(t, h, "leo-alpha"),
		"leo-renamed": mustOpen(t, h, "leo-old.ab12"),
	}
	r := &Router{Hub: h, Targets: func(name string) (Target, bool) { tg, ok := targets[name]; return tg, ok }}

	if _, ok := r.Route("leo-alpha"); ok {
		t.Fatal("routed an agent whose bridge is not connected")
	}
	_ = mustConnect(t, h, "leo-alpha")
	if target, ok := r.Route("leo-alpha"); !ok || target != targets["leo-alpha"] {
		t.Fatalf("Route(leo-alpha) = %+v, %v; want leo-alpha's live generation", target, ok)
	}
	_ = mustConnect(t, h, "leo-old.ab12")
	if target, ok := r.Route("leo-renamed"); !ok || target.Key != "leo-old.ab12" {
		t.Fatalf("Route(leo-renamed) = %+v, %v; want its launch key", target, ok)
	}
	if _, ok := r.Route("leo-unknown"); ok {
		t.Fatal("routed an agent with no bridge key")
	}
	if key, ok := r.Key("leo-renamed"); !ok || key != "leo-old.ab12" {
		t.Fatalf("Key(leo-renamed) = %q, %v", key, ok)
	}
}

// An agent is routed only to its own launch's generation: if another
// launch has opened its key since (a renamed agent's surviving session
// adopted under it), the agent's messages must not reach that one.
func TestRouterNeverRoutesToAnotherLaunchsGeneration(t *testing.T) {
	h := newTestHub(newFakeClock())
	own := mustOpen(t, h, "leo-alpha")
	r := &Router{Hub: h, Targets: func(string) (Target, bool) { return own, true }}
	if _, err := h.Open("leo-alpha", "launch-other"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Connect("leo-alpha", "launch-other"); err != nil {
		t.Fatal(err)
	}
	if target, ok := r.Route("leo-alpha"); ok {
		t.Fatalf("routed to %+v, another launch's generation", target)
	}
}

func TestNilRouterNeverRoutes(t *testing.T) {
	var r *Router
	if _, ok := r.Route("leo-alpha"); ok {
		t.Fatal("nil router routed")
	}
	if _, ok := r.Key("leo-alpha"); ok {
		t.Fatal("nil router knew a key")
	}
	if (&Router{}).Connected("x") {
		t.Fatal("hub-less router reported a connection")
	}
}
