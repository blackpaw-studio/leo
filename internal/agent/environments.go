package agent

import (
	"errors"
	"fmt"
	"log"
	"slices"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// PersistentTaskError reports a set-environment refused because the agent
// backs a persistent task, whose environments belong in config.
type PersistentTaskError struct{ Agent, Task string }

func (e *PersistentTaskError) Error() string {
	return fmt.Sprintf("agent %q backs persistent task %q — set tasks.%s.environments in config instead of overriding the agent", e.Agent, e.Task, e.Task)
}

// HarnessMismatchError reports a set-environment refused because the agent's
// harness differs from the one its template is now configured for: the rebuilt
// launch would not fit the running agent.
type HarnessMismatchError struct{ Agent, Template, Have, Want string }

func (e *HarnessMismatchError) Error() string {
	return fmt.Sprintf("harness mismatch: agent %q runs on %s but template %q is now configured for %s — switch it to another template ('leo agent set-template') or re-create it first, then change its environments",
		e.Agent, e.Have, e.Template, e.Want)
}

// SetEnvironmentsResult describes what SetEnvironments did.
type SetEnvironmentsResult struct {
	Name string `json:"name"`
	// From and To are the agent's override before and after; nil means "the
	// template's (or defaults') list applies".
	From []string `json:"from,omitempty"`
	To   []string `json:"to,omitempty"`
	// Effective is the ordered list of environments the agent now runs under.
	Effective []string `json:"effective,omitempty"`
	// Status is "running" for a live agent that was stopped and respawned,
	// "stopped" for a dormant agent whose record was rewritten in place.
	Status string `json:"status"`
	// Unchanged marks a no-op: the agent already had exactly this override.
	Unchanged bool `json:"unchanged,omitempty"`
}

// SetEnvironments re-points an existing agent at a different ordered list of
// named environments (an empty list clears the override, so the template's
// default applies), rebuilding its env from current config and restarting it
// with its conversation resumed.
//
// A live agent is stopped and respawned like SwitchTemplate does it: the env
// is rebuilt from scratch (keys of the departing environment disappear), the
// session id is read BEFORE the record's env changes — the conversation's
// transcript still sits under the old account's config dir at that point —
// and the respawn resumes it. Whether the new account can actually load that
// transcript depends on `projects` being shared between config dirs (see the
// environments guide); if it cannot, claude's quick-exit recovery drops the
// resume and the agent starts a fresh conversation. A dormant agent is
// rewritten in place and picks the environments up on its next Start.
//
// The record is saved before the respawn so a daemon restart racing this call
// sees the new environments; a failed respawn therefore marks the agent
// dormant ('leo agent start' recovers it), exactly as SwitchTemplate does.
func (m *Manager) SetEnvironments(name string, names []string) (SetEnvironmentsResult, error) {
	if len(names) == 0 {
		names = nil
	}
	cfg, err := m.cfgLoader()
	if err != nil {
		return SetEnvironmentsResult{}, fmt.Errorf("loading config: %w", err)
	}
	stored, err := agentstore.Load(agentstore.FilePath(cfg.HomePath))
	if err != nil {
		return SetEnvironmentsResult{}, fmt.Errorf("loading agentstore: %w", err)
	}
	rec, ok := stored[name]
	if !ok {
		return SetEnvironmentsResult{}, fmt.Errorf("no agentstore record for %q (cannot set the environments of an unpersisted agent)", name)
	}
	if task, bound := persistentTaskFor(cfg, name); bound {
		return SetEnvironmentsResult{}, &PersistentTaskError{Agent: name, Task: task}
	}
	tmpl, ok := cfg.Templates[rec.Template]
	if !ok {
		return SetEnvironmentsResult{}, fmt.Errorf("template %q of agent %q is not in config, so its environment cannot be re-resolved", rec.Template, name)
	}

	if cfgHarness, recHarness := normalizeHarness(cfg.TemplateHarness(tmpl)), normalizeHarness(rec.Harness); cfgHarness != recHarness {
		return SetEnvironmentsResult{}, &HarnessMismatchError{Agent: name, Template: rec.Template, Have: recHarness, Want: cfgHarness}
	}

	_, live := m.sup.EphemeralAgents()[name]
	status := "running"
	if !live {
		if !rec.Stopped {
			return SetEnvironmentsResult{}, fmt.Errorf("agent %q is neither running nor stopped (its record may be stale)", name)
		}
		status = "stopped"
	}

	effective := cfg.EnvironmentNames(names, tmpl.Environments)
	result := SetEnvironmentsResult{Name: name, From: rec.Environments, To: names, Effective: effective, Status: status}
	if slices.Equal(rec.Environments, names) {
		result.Unchanged = true
		return result, nil
	}
	if _, err := cfg.MergeEnvironments(effective); err != nil {
		return SetEnvironmentsResult{}, err
	}
	if err := cfg.ValidateEnvironmentNames(names); err != nil {
		return SetEnvironmentsResult{}, err
	}

	// Read the conversation to resume BEFORE the record's env points at the
	// new account: ResumeIDFor scans the transcript dir the env resolves to.
	resumeID := ResumeIDFor(rec)
	isClaude := normalizeHarness(rec.Harness) == "claude"

	next := rec
	next.Environments = names
	args, env, err := resolveTemplateWiring(cfg, next, tmpl, m.webToken, m.leoMCP, rebuildEnvFromTemplate)
	if errors.Is(err, errWiringNotBuilt) {
		return SetEnvironmentsResult{}, fmt.Errorf("building %s wiring for agent %q failed (agent left as it was; see the daemon log)", normalizeHarness(rec.Harness), name)
	}
	if err != nil {
		return SetEnvironmentsResult{}, fmt.Errorf("setting environments of %q: %w", name, err)
	}
	if isClaude {
		args = ResumeArgs(args, resumeID)
	}
	next.ClaudeArgs, next.Env, next.EnvLayered = args, env, true
	next.SessionID = resumeID
	next.SessionPinnedAt = nil
	next.OpeningBriefID = "" // args were rebuilt promptless; see Start

	if status == "stopped" {
		if err := agentstore.Save(cfg.HomePath, next); err != nil {
			return SetEnvironmentsResult{}, fmt.Errorf("saving agent record: %w", err)
		}
		m.publishEnvironmentsChanged(cfg, next, "stopped")
		return result, nil
	}

	if err := m.sup.StopAgent(name, false); err != nil {
		return SetEnvironmentsResult{}, fmt.Errorf("stopping agent to change its environments: %w", err)
	}
	if err := agentstore.Save(cfg.HomePath, next); err != nil {
		return SetEnvironmentsResult{}, m.relaunchAfterFailedSave(cfg.HomePath, rec, resumeID, isClaude, m.sup.SpawnAgent, err)
	}
	if err := m.sup.SpawnAgent(SpawnRequest{
		Name:       next.Name,
		ClaudeArgs: next.ClaudeArgs,
		WorkDir:    next.Workspace,
		Env:        next.Env,
		WebPort:    next.WebPort,
		WebToken:   m.webToken,
		Harness:    next.Harness,
	}); err != nil {
		down := next
		down.Stopped = true
		down.WakeOnMessage = false
		if saveErr := agentstore.Save(cfg.HomePath, down); saveErr != nil {
			log.Printf("agent %q: could not mark stopped after a failed environment change: %v", name, saveErr)
		}
		return SetEnvironmentsResult{}, fmt.Errorf("respawning %q on its new environments: %w (the agent is stopped — 'leo agent start %s' brings it back)", name, err, name)
	}
	m.publishEnvironmentsChanged(cfg, next, "starting")
	return result, nil
}

// publishEnvironmentsChanged announces the agent's new environments on the
// observability stream as an ordinary agent_state_changed, so a subscriber
// updates its row without refetching the snapshot.
func (m *Manager) publishEnvironmentsChanged(cfg *config.Config, rec agentstore.Record, rawStatus string) {
	names, source, _ := ResolveAgentEnvironments(cfg, rec.Name, rec.Template, rec.Environments)
	restarts := 0
	if st, ok := m.sup.EphemeralAgents()[rec.Name]; ok {
		restarts = st.Restarts
	}
	status, wake := observe.AgentDormancy(rawStatus, rec.WakeOnMessage)
	m.publish(observe.Event{
		Type: observe.EventAgentStateChanged,
		Payload: &observe.AgentStateChangedPayload{
			Agent:              rec.Name,
			Status:             status,
			Restarts:           restarts,
			WakeOnMessage:      wake,
			Environments:       names,
			EnvironmentsSource: source,
		},
	})
}

// relaunchAfterFailedSave undoes a set-environment whose record could not be
// saved after the agent was already stopped. The stored record still describes
// the old launch, so the agent comes back on it (resuming the conversation, as
// the new launch would have) and stays start-able either way. Returns the error
// SetEnvironments reports.
func (m *Manager) relaunchAfterFailedSave(homePath string, old agentstore.Record, resumeID string, isClaude bool, spawn func(SpawnRequest) error, saveErr error) error {
	args := old.ClaudeArgs
	if isClaude {
		args = ResumeArgs(args, resumeID)
	}
	if err := spawn(SpawnRequest{
		Name:       old.Name,
		ClaudeArgs: args,
		WorkDir:    old.Workspace,
		Env:        old.Env,
		WebPort:    old.WebPort,
		WebToken:   m.webToken,
		Harness:    old.Harness,
	}); err != nil {
		down := old
		down.Stopped, down.WakeOnMessage = true, false
		if markErr := agentstore.Save(homePath, down); markErr != nil {
			log.Printf("agent %q: could not mark stopped after a failed environment change: %v", old.Name, markErr)
		}
		return fmt.Errorf("saving agent record: %w; relaunching %q on its old environments also failed: %v (the agent is down — 'leo agent start %s' brings it back)", saveErr, old.Name, err, old.Name)
	}
	return fmt.Errorf("saving agent record: %w (the agent was relaunched on its old environments; nothing changed)", saveErr)
}
