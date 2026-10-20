package web

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/drain"
)

// A busy bridged agent's pause notice is queued under the drain's own
// command id, so the drain can ask the outbox about it later.
func TestDrainNotifierBridgedQueuedKeepsTheID(t *testing.T) {
	s, _ := newTestServer(t)
	old := bridgeMessageWait
	bridgeMessageWait = 30 * time.Millisecond
	t.Cleanup(func() { bridgeMessageWait = old })
	b := newBridgedAgent(t, s, "assistant", true)
	type result struct {
		d   drain.Delivery
		err error
	}
	done := make(chan result, 1)
	go func() {
		d, err := s.DrainNotifier().Message(context.Background(), "assistant", "[leo] pause", "drain-x-assistant")
		done <- result{d, err}
	}()
	cmd := b.next()
	if cmd.ID != "drain-x-assistant" || cmd.Text != "[leo] pause" {
		t.Fatalf("deliver = %+v, want id drain-x-assistant", cmd)
	}
	r := <-done
	if r.err != nil {
		t.Fatalf("Message: %v", r.err)
	}
	if !r.d.Queued || r.d.Transport != transportBridge {
		t.Fatalf("delivery = %+v, want queued over the bridge", r.d)
	}
	b.ack(cmd, true, "")
}

func TestDrainNotifierHoldsAsksTheOutbox(t *testing.T) {
	s, _ := newTestServer(t)
	newBridgedAgent(t, s, "assistant", true)
	var asked string
	s.bridgeRouter.Held = func(agent, id string) (bool, error) {
		asked = agent + "/" + id
		return true, nil
	}
	held, err := s.DrainNotifier().Holds("assistant", "drain-x-assistant")
	if err != nil || !held {
		t.Fatalf("Holds = %v, %v; want true, nil", held, err)
	}
	if asked != "assistant/drain-x-assistant" {
		t.Fatalf("outbox asked about %q", asked)
	}
}

func TestDrainNotifierLegacyDelivery(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	s.resolvePeerSocket = func(context.Context, string) (string, error) { return "/tmp/peer.sock", nil }
	var got string
	s.deliverPeer = func(_ context.Context, _ string, text string) error {
		got = text
		return nil
	}
	d, err := s.DrainNotifier().Message(context.Background(), "assistant", "[leo] pause", "drain-x-assistant")
	if err != nil {
		t.Fatalf("Message: %v", err)
	}
	if d.Queued || d.Transport != transportLegacy {
		t.Fatalf("delivery = %+v, want an immediate legacy delivery", d)
	}
	if got != "[leo] pause" {
		t.Fatalf("delivered %q", got)
	}
}

func TestDrainNotifierUnknownAgentFails(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	_, err := s.DrainNotifier().Message(context.Background(), "ghost", "[leo] pause", "")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("err = %v, want a no-such-agent error", err)
	}
}

// Running dispatches go to the drain by name (their id when unnamed),
// marked as dispatches so it lists them without messaging them.
func TestDispatchProbesNameRunningDispatches(t *testing.T) {
	got := dispatchProbes([]consult.Record{{ID: "d-1", Name: "fix-x"}, {ID: "d-2"}})
	want := []drain.Probe{
		{Name: "fix-x", Exists: true, Busy: true, Kind: drain.KindDispatch},
		{Name: "d-2", Exists: true, Busy: true, Kind: drain.KindDispatch},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dispatchProbes = %+v, want %+v", got, want)
	}
}

func TestRunningDispatchesWithoutADispatcherIsEmpty(t *testing.T) {
	if got := (&Server{}).RunningDispatches(); len(got) != 0 {
		t.Fatalf("RunningDispatches = %+v, want none", got)
	}
}
