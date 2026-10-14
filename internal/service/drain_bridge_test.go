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
func TestAwaitAgentBridgeWaitsForTheRouteToBePlanned(t *testing.T) {
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

	awaitAgentBridge(route, bridge.New(bridge.Options{}), 200*time.Millisecond)(context.Background(), "lgr")

	if got := calls.Load(); got < 2 {
		t.Fatalf("route read %d time(s); want the wait to keep reading until the route is planned", got)
	}
}
