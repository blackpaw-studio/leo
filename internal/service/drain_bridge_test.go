package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

// A restored agent's launch decides its bridge route on its supervise
// goroutine, after restore returns: the resume must wait for that decision
// instead of reading the not-yet-planned route as "no bridge".
func TestAwaitResumeBridgeWaitsForTheRouteToBePlanned(t *testing.T) {
	var calls atomic.Int32
	route := func(name string) BridgeRoute {
		if name != "lgr" {
			t.Errorf("route(%q); want lgr", name)
		}
		if calls.Add(1) == 1 {
			return BridgeRoute{} // adoption still in flight
		}
		return BridgeRoute{Planned: true} // settled on a legacy launch
	}

	err := awaitResumeBridge(context.Background(), route, bridge.New(bridge.Options{}), "lgr", 200*time.Millisecond)

	if err != nil {
		t.Fatalf("err = %v; want a legacy launch to settle", err)
	}
	if got := calls.Load(); got < 2 {
		t.Fatalf("route read %d time(s); want the wait to keep reading until the route is planned", got)
	}
}

// bridgedRoute opens a generation of key on hub and returns the route of
// the launch that holds it.
func bridgedRoute(t *testing.T, hub *bridge.Hub, key string) func(string) BridgeRoute {
	t.Helper()
	target, err := hub.Open(key, rigLaunch)
	if err != nil {
		t.Fatal(err)
	}
	return func(string) BridgeRoute { return BridgeRoute{Target: target, Planned: true} }
}

// The web routes a message over the bridge only to a live stream. A mod
// that said hello but whose stream is not up yet would have the resume
// fall back to the legacy path, so the wait is for the stream itself.
func TestAwaitResumeBridgeWaitsForTheStreamNotJustAHello(t *testing.T) {
	hub := bridge.New(bridge.Options{})
	route := bridgedRoute(t, hub, "lgr")
	if err := hub.Apply("lgr", rigLaunch, bridge.Report{Type: bridge.ReportHello, SessionID: "s-1"}); err != nil {
		t.Fatal(err)
	}

	err := awaitResumeBridge(context.Background(), route, hub, "lgr", 20*time.Millisecond)

	if err == nil {
		t.Fatal("settled on a hello with no stream; want it to wait for the stream and time out")
	}
}

func TestAwaitResumeBridgeSettlesOnALiveStream(t *testing.T) {
	hub := bridge.New(bridge.Options{})
	route := bridgedRoute(t, hub, "lgr")
	if _, err := hub.Connect("lgr", rigLaunch); err != nil {
		t.Fatal(err)
	}

	if err := awaitResumeBridge(context.Background(), route, hub, "lgr", time.Second); err != nil {
		t.Fatalf("err = %v; want a connected launch to settle", err)
	}
}
