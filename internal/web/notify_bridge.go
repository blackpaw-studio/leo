package web

import (
	"context"
	"fmt"
	"strings"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// notificationDelivery is how dispatch completion notifications reach their
// callers: over a bridged caller's leo bridge when router is set, else (and
// for every other caller) the peer inbox or tmux, as before.
func (s *Server) notificationDelivery(router *bridge.Router) consult.NotificationDelivery {
	legacy := consult.NewTmuxNotificationDelivery(findTmuxPath(), s.execCommandContext)
	if router == nil {
		return legacy
	}
	return consult.NewBridgeNotificationDelivery(router, s.agentPrimaryPane, s.bridgeKeyOwner, legacy)
}

// agentPrimaryPane returns the pane agent name's harness runs in, as its
// session recorded at launch (@leo_primary_pane).
func (s *Server) agentPrimaryPane(ctx context.Context, name string) (string, error) {
	session := agent.SessionName(name)
	out, err := s.execCommandContext(ctx, findTmuxPath(), tmux.Args("show-options", "-t", tmux.Target(session)+":", "-v", "@leo_primary_pane")...).Output()
	if err != nil {
		return "", fmt.Errorf("reading %s's primary pane: %w", session, err)
	}
	pane := strings.TrimSpace(string(out))
	if pane == "" {
		return "", fmt.Errorf("%s has no primary pane recorded", session)
	}
	return pane, nil
}

// bridgeKeyOwner returns the agent whose launch holds bridge key key now,
// whatever it has been renamed to since: whose outbox keeps its messages.
func (s *Server) bridgeKeyOwner(key string) (string, bool) {
	if s.processes == nil || s.bridgeRouter == nil {
		return "", false
	}
	for name := range s.processes.States() {
		if held, ok := s.bridgeRouter.Key(name); ok && held == key {
			return name, true
		}
	}
	return "", false
}
