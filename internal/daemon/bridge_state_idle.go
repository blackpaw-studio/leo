package daemon

import (
	"context"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/consult"
)

// defaultIdleBridgeStateInterval matches the web server's state tick.
const defaultIdleBridgeStateInterval = time.Second

// startIdleBridgeState keeps bridged mods supplied with state when the web
// server, which owns the dispatch-aware pusher, is off (a host with no `web:`
// section, e.g. a Leo.app laptop). A mod that gets no state line within its
// wait treats the policy as unknown and warns "no state from leo".
//
// Without the web server there is no dispatcher, so the snapshot carries no
// dispatches and delegation off: native agents stay available rather than
// hidden behind dispatch tools that cannot run. It stops with the daemon's
// parent context.
func (s *Server) startIdleBridgeState() {
	interval := s.bridgeStateInterval
	if interval <= 0 {
		interval = defaultIdleBridgeStateInterval
	}
	pusher := &consult.StatePusher{
		Hub:        s.bridge,
		Records:    func() []consult.Record { return nil },
		Delegation: func() bridge.DelegationState { return bridge.DelegationState{} },
		Now:        time.Now,
	}
	go func(ctx context.Context) {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pusher.Tick()
			}
		}
	}(s.parentContext)
}
