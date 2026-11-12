package web

import (
	"context"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/harness/codex"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// codexUsageLister lists the running codex agents whose rollouts the usage
// poller reads: with a live process (a suspended or stopped agent changes
// nothing), the supervisor's own start time (the record's can be stale),
// the agent's CODEX_HOME and the session id leo stored for it, if any.
type codexUsageLister struct {
	list    func() []agent.Record
	states  func() map[string]ProcessStateInfo
	resolve func(name string) (harnessName string, h harness.SessionHandle, ok bool)
}

// CodexAgents implements observe.CodexAgentLister.
func (l codexUsageLister) CodexAgents() []observe.CodexAgent {
	states := l.states()
	var out []observe.CodexAgent
	for _, rec := range l.list() {
		status, startedAt := rec.Status, rec.StartedAt
		if st, ok := states[rec.Name]; ok {
			status = st.Status
			if !st.StartedAt.IsZero() {
				startedAt = st.StartedAt
			}
		}
		if status != "running" {
			continue
		}
		harnessName, h, ok := l.resolve(rec.Name)
		if !ok || harnessName != "codex" {
			continue
		}
		a := observe.CodexAgent{Name: rec.Name, Home: codex.CodexHome(h.Env), Workspace: h.Workspace, StartedAt: startedAt}
		if h.IDs != nil {
			a.SessionID = h.IDs.Get()
		}
		out = append(out, a)
	}
	return out
}

// startCodexUsagePoller runs the daemon-wide codex usage poller into the
// bridge feed until ctx ends; the returned channel closes once it has
// stopped (at once when there is nothing to poll).
func (s *Server) startCodexUsagePoller(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	sink, ok := s.bridgeFeed.(observe.HarnessUsageSink)
	if !ok || s.agentSvc == nil {
		close(done)
		return done
	}
	lister := codexUsageLister{list: s.agentSvc.List, states: s.processStates, resolve: s.agentSvc.ResolveHandle}
	poller := observe.NewCodexPoller(lister, observe.CodexLocators{ByID: codex.RolloutPath, Latest: codex.LatestRolloutFor}, sink, observe.CodexPollerOptions{})
	go func() {
		defer close(done)
		poller.Run(ctx)
	}()
	return done
}
