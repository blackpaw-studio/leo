package web

import (
	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/harness/claude/bridgemod"
)

// BridgeOptions wires the claude mod bridge into the web server: Router
// (whose Hub is the daemon's bridge hub) routes agent names to their live
// bridges, Launcher plans bridged dispatch launches. The zero value leaves
// every agent and dispatch on tmux.
type BridgeOptions struct {
	Router   *bridge.Router
	Launcher *bridgemod.Launcher
}

func (o BridgeOptions) hub() *bridge.Hub {
	if o.Router == nil {
		return nil
	}
	return o.Router.Hub
}

// dispatchBridge says which dispatches take their turn state from the
// bridge rather than the claude shell hooks (consult.TmuxInteractiveRuntime).
type dispatchBridge interface {
	BridgeOwnsReports(id string) bool
}
