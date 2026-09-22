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

// SetAttention wires the per-agent attention store the supervisor drives on
// launch, unexpected exit, and stop. Optional: nil disables attention.
func (s *Supervisor) SetAttention(a *observe.AttentionStore) {
	s.mu.Lock()
	s.attention = a
	s.mu.Unlock()
}

// attentionStore returns the wired store (possibly nil; its methods are
// nil-safe).
func (s *Supervisor) attentionStore() *observe.AttentionStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.attention
}

// markAttention transitions an already-tracked agent's attention, but only
// while id is still that name's registered live generation. Unlike
// isStaleLocked, a missing identity counts as stale here: StopAgent removes
// it before recording its own transition, and a dying goroutine must not
// overwrite that.
func (s *Supervisor) markAttention(name string, id *procIdentity, state observe.AttentionState) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if cur, ok := s.identities[name]; !ok || cur != id {
		return
	}
	s.attention.SetIfTracked(name, state)
}

// launchAttention sets name's attention on launch/adopt, for the live
// generation only.
func (s *Supervisor) launchAttention(name string, id *procIdentity, state observe.AttentionState) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if cur, ok := s.identities[name]; !ok || cur != id {
		return
	}
	s.attention.Set(name, state)
}

// dropAttention removes name's attention when its live generation launched
// without hooks, so the field reads absent rather than stale.
func (s *Supervisor) dropAttention(name string, id *procIdentity) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if cur, ok := s.identities[name]; !ok || cur != id {
		return
	}
	s.attention.Remove(name)
}

// attentionTracked reports whether name currently has attention.
func (s *Supervisor) attentionTracked(name string) bool {
	_, ok := s.attentionStore().Get(name)
	return ok
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
	if att, ok := s.attentionStore().Get(spec.Name); ok {
		a.Attention = &att
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
