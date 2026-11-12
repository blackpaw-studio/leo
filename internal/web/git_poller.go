package web

import (
	"context"
	"log"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// agentGitLister lists the agents whose workspace the git poller reads:
// those with a live process. A suspended or stopped agent changes nothing.
type agentGitLister struct {
	list func() []agent.Record
}

// GitAgents implements observe.GitAgentLister.
func (l agentGitLister) GitAgents() []observe.GitAgent {
	var out []observe.GitAgent
	for _, rec := range l.list() {
		if rec.Status != "running" || rec.Workspace == "" {
			continue
		}
		out = append(out, observe.GitAgent{Name: rec.Name, Workspace: rec.Workspace})
	}
	return out
}

// startGitPoller runs the daemon-wide git stats poller into the bridge feed
// until ctx ends; the returned channel closes once it has stopped (at once
// when there is nothing to poll).
func (s *Server) startGitPoller(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	sink, ok := s.bridgeFeed.(observe.GitStatsSink)
	if !ok || s.agentSvc == nil {
		close(done)
		return done
	}
	poller := observe.NewGitPoller(agentGitLister{list: s.agentSvc.List}, observe.ExecCommandRunner{}, sink, observe.GitPollerOptions{Logf: log.Printf})
	go func() {
		defer close(done)
		poller.Run(ctx)
	}()
	return done
}
