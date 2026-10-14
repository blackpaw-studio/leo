package daemon

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// turnRig is a router with an async (nil-Result) injector, as every real
// tmux-TUI driver is, and one invocation in flight on session "agent".
type turnRig struct {
	r       *sessionRouter
	inv     *PendingInvocation
	aborted *int32
}

const turnRigPrompt = "summarize the inbox\n\nthen reply OK"

// newTurnRig enqueues one invocation and waits until the pump has injected
// it. release, when non-nil, holds the injector open until it is closed.
func newTurnRig(t *testing.T, release <-chan struct{}) turnRig {
	t.Helper()
	r := newSessionRouter()
	t.Cleanup(r.Stop)
	injected := make(chan struct{})
	var once sync.Once
	var aborted int32
	r.SetInjector(func(context.Context, string, string) (*harness.Result, error) {
		once.Do(func() { close(injected) })
		if release != nil {
			<-release
		}
		return nil, nil
	})
	r.SetAborter(func(string) error {
		atomic.AddInt32(&aborted, 1)
		return nil
	})
	inv, ok := r.Enqueue(EnqueueParams{Session: "agent", TmuxSession: "leo-agent", Task: "t", Prompt: turnRigPrompt, QueueMax: 5, Timeout: time.Hour})
	if !ok {
		t.Fatal("enqueue rejected")
	}
	r.StartPump("agent")
	<-injected
	return turnRig{r: r, inv: inv, aborted: &aborted}
}

func (g turnRig) result(t *testing.T) InvocationResult {
	t.Helper()
	select {
	case res := <-g.inv.Result:
		return res
	case <-time.After(2 * time.Second):
		t.Fatal("invocation never completed")
		return InvocationResult{}
	}
}

func (g turnRig) assertPending(t *testing.T) {
	t.Helper()
	// Every ObserveTurn call is synchronous, so a completion would already
	// sit in the buffered Result channel.
	select {
	case res := <-g.inv.Result:
		t.Fatalf("invocation completed unexpectedly: %+v", res)
	default:
	}
	if _, ok := g.r.InFlight("agent"); !ok {
		t.Fatal("invocation no longer in flight")
	}
}

func TestRouterCompletesInFlightOnMatchedAgentTurn(t *testing.T) {
	g := newTurnRig(t, nil)

	// The harness echoes the prompt with its own whitespace (a TUI paste
	// may fold newlines); the match normalizes it.
	g.r.ObserveTurn("agent", TurnEvent{Event: "UserPromptSubmit", Prompt: "summarize the inbox then reply OK ", TurnID: "turn-x", SessionID: "sess-1"})
	g.r.ObserveTurn("agent", TurnEvent{Event: "Stop", TurnID: "turn-x", SessionID: "sess-1", LastMessage: "OK"})

	res := g.result(t)
	want := InvocationResult{OK: true, SessionID: "sess-1", FinalMessage: "OK"}
	if res != want {
		t.Fatalf("result = %+v, want %+v", res, want)
	}
	if _, ok := g.r.InFlight("agent"); ok {
		t.Fatal("invocation still in flight after its turn stopped")
	}
	if n := atomic.LoadInt32(g.aborted); n != 0 {
		t.Fatalf("aborter called %d times, want 0", n)
	}
}

func TestRouterIgnoresUnmatchedTurn(t *testing.T) {
	g := newTurnRig(t, nil)

	// A prompt a human typed into the pane is not the invocation's turn.
	g.r.ObserveTurn("agent", TurnEvent{Event: "UserPromptSubmit", Prompt: "what time is it?", TurnID: "human"})
	g.r.ObserveTurn("agent", TurnEvent{Event: "Stop", TurnID: "human", LastMessage: "noon"})
	g.assertPending(t)

	// The invocation's turn is bound, but a Stop naming another turn is not
	// its completion.
	g.r.ObserveTurn("agent", TurnEvent{Event: "UserPromptSubmit", Prompt: turnRigPrompt, TurnID: "turn-x"})
	g.r.ObserveTurn("agent", TurnEvent{Event: "Stop", TurnID: "turn-y", LastMessage: "other"})
	g.assertPending(t)

	// Hooks for a session with nothing in flight are dropped.
	g.r.ObserveTurn("someone-else", TurnEvent{Event: "Stop", TurnID: "turn-x"})
	g.assertPending(t)

	g.r.ObserveTurn("agent", TurnEvent{Event: "Stop", TurnID: "turn-x", LastMessage: "OK"})
	if res := g.result(t); !res.OK || res.FinalMessage != "OK" {
		t.Fatalf("result = %+v, want OK with the bound turn's message", res)
	}
}

func TestRouterInterruptFailsMatchedTurn(t *testing.T) {
	g := newTurnRig(t, nil)

	g.r.ObserveTurn("agent", TurnEvent{Event: "UserPromptSubmit", Prompt: turnRigPrompt, TurnID: "turn-x", SessionID: "sess-1"})
	g.r.ObserveTurn("agent", TurnEvent{Event: "Interrupt", TurnID: "turn-x", SessionID: "sess-1"})

	res := g.result(t)
	want := InvocationResult{OK: false, SessionID: "sess-1", Err: "interrupted"}
	if res != want {
		t.Fatalf("result = %+v, want %+v", res, want)
	}
}

func TestRouterHookCompletionDuringInjectDoesNotAbort(t *testing.T) {
	release := make(chan struct{})
	g := newTurnRig(t, release)

	// A fast turn: submit and Stop land before the injector returns.
	g.r.ObserveTurn("agent", TurnEvent{Event: "UserPromptSubmit", Prompt: turnRigPrompt, TurnID: "turn-x"})
	g.r.ObserveTurn("agent", TurnEvent{Event: "Stop", TurnID: "turn-x", LastMessage: "OK"})
	close(release)

	if res := g.result(t); !res.OK {
		t.Fatalf("result = %+v, want OK", res)
	}
	// Wait for the pump to pass its post-inject check: a second invocation
	// injecting proves it moved on.
	next, _ := g.r.Enqueue(EnqueueParams{Session: "agent", Task: "t2", Prompt: "next", QueueMax: 5, Timeout: time.Hour})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if inv, ok := g.r.InFlight("agent"); ok && inv.ID == next.ID {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pump never advanced to the next invocation")
		}
		time.Sleep(time.Millisecond)
	}
	if n := atomic.LoadInt32(g.aborted); n != 0 {
		t.Fatalf("aborter called %d times, want 0: a turn that completed was aborted", n)
	}
}
