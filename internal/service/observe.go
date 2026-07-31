package service

import (
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// SetPublisher wires an observe.Publisher into the supervisor so agent
// lifecycle transitions are announced on the event bus. Optional: an unset
// (nil) publisher makes every publish call a safe no-op, so existing
// NewSupervisor callers are unaffected.
func (s *Supervisor) SetPublisher(p observe.Publisher) {
	s.mu.Lock()
	s.publisher = p
	s.mu.Unlock()
}

// publish is a nil-safe no-op when no publisher has been configured.
func (s *Supervisor) publish(ev observe.Event) {
	s.mu.RLock()
	p := s.publisher
	s.mu.RUnlock()
	if p == nil {
		return
	}
	p.Publish(ev)
}

// SessionNames returns the current agent-name -> tmux-session-name mapping
// for every live ephemeral agent, satisfying the accessor observe.NewTracker
// needs to sweep tmux activity. Reads through the live procIdentity handles
// (the same source RenameAgent keeps in sync) rather than deriving the
// session name a second time, so the "leo-<name>" convention stays defined in
// exactly one place (agent.SessionName, via procIdentity.SessionName).
func (s *Supervisor) SessionNames() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]string, len(s.identities))
	for name, id := range s.identities {
		result[name] = id.SessionName()
	}
	return result
}

// spawnedAgentView builds the observe.Agent carried on an agent_spawned
// event. Template/Repo/Branch come from the agentstore record for spec.Name
// — agent.Manager persists that record BEFORE calling SpawnAgent (see its
// doc comment), so it is reliably present by the time we publish here — and
// Model comes from the defaults->template->agent cascade via s.configPath.
// Both lookups degrade to zero-valued fields (never guessed) if the record
// or config isn't available, e.g. in tests that construct a bare Supervisor.
func (s *Supervisor) spawnedAgentView(spec daemon.AgentSpawnSpec, spawnedAt time.Time) observe.Agent {
	a := observe.Agent{
		Name:      spec.Name,
		Workspace: spec.WorkDir,
		Harness:   spec.Harness,
		Status:    observe.StatusStarting,
		StartedAt: spawnedAt,
	}

	if records, err := agentstore.Load(agentstore.FilePath(s.homePath)); err == nil {
		if rec, ok := records[spec.Name]; ok {
			a.Template = rec.Template
			a.Repo = rec.Repo
			a.Branch = rec.Branch
		}
	}

	if a.Template != "" && s.configPath != "" {
		if cfg, err := config.Load(s.configPath); err == nil {
			if tmpl, ok := cfg.Templates[a.Template]; ok {
				a.Model = cfg.TemplateModel(tmpl)
			}
		}
	}

	return a
}

// toObserveStatus maps the supervisor's internal status vocabulary onto the
// wire contract's lifecycle enum. "restarting" — a purely internal
// crash-loop-backoff state — folds into "starting", the closest lifecycle
// equivalent a consumer can act on.
func toObserveStatus(status string) observe.Status {
	switch status {
	case "running":
		return observe.StatusRunning
	case "starting", "restarting":
		return observe.StatusStarting
	case "stopped":
		return observe.StatusStopped
	default:
		return observe.Status(status)
	}
}
