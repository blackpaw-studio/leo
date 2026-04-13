// Package agent owns the lifecycle of ephemeral Leo agents — template resolution,
// workspace setup, claude arg construction, supervisor registration, and persistence.
// It is consumed by the web UI, the daemon socket handlers, and the CLI, so all three
// share a single source of truth.
package agent

import (
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
)

// Supervisor is the subset of service.Supervisor that the Manager needs.
// Defined here so callers inject an implementation.
type Supervisor interface {
	SpawnAgent(spec SpawnRequest) error
	StopAgent(name string) error
	EphemeralAgents() map[string]ProcessState
}

// ConfigLoader returns the current config. It is invoked on every Manager call so
// the Manager picks up config edits without a restart.
type ConfigLoader func() (*config.Config, error)

// Manager is the central agent-lifecycle component.
type Manager struct {
	cfgLoader ConfigLoader
	sup       Supervisor
	tmuxPath  string
}

// New constructs a Manager. tmuxPath is used for Logs (tmux capture-pane); pass the
// empty string to have the Manager look up tmux from $PATH on demand.
func New(cfgLoader ConfigLoader, sup Supervisor, tmuxPath string) *Manager {
	return &Manager{cfgLoader: cfgLoader, sup: sup, tmuxPath: tmuxPath}
}

// SpawnSpec describes a spawn request in terms of high-level intent.
type SpawnSpec struct {
	Template string // required — template name from config.Templates
	Repo     string // required — owner/repo (clones) OR a plain name (uses template workspace)
	Name     string // optional — overrides the derived agent name
}

// Record is the public view of an agent, merging persisted metadata with live state.
type Record struct {
	Name      string            `json:"name"`
	Template  string            `json:"template,omitempty"`
	Repo      string            `json:"repo,omitempty"`
	Workspace string            `json:"workspace,omitempty"`
	Status    string            `json:"status,omitempty"`
	StartedAt time.Time         `json:"started_at,omitempty"`
	Restarts  int               `json:"restarts,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

// Spawn resolves a template + repo into a running agent.
// On success the agent is both registered with the supervisor and persisted to agentstore.
// The persistence write happens *after* a successful supervisor spawn so a failed spawn
// cannot leave orphaned records behind (addresses audit finding #10).
func (m *Manager) Spawn(spec SpawnSpec) (Record, error) {
	if spec.Template == "" || spec.Repo == "" {
		return Record{}, fmt.Errorf("template and repo are required")
	}
	if err := ValidateRepo(spec.Repo); err != nil {
		return Record{}, err
	}

	cfg, err := m.cfgLoader()
	if err != nil {
		return Record{}, fmt.Errorf("loading config: %w", err)
	}
	tmpl, ok := cfg.Templates[spec.Template]
	if !ok {
		return Record{}, fmt.Errorf("template %q not found", spec.Template)
	}

	workspace, agentName, err := ResolveWorkspace(tmpl, spec.Template, spec.Repo, spec.Name)
	if err != nil {
		return Record{}, err
	}

	existing := m.sup.EphemeralAgents()
	if _, collision := existing[agentName]; collision {
		for i := 2; ; i++ {
			candidate := fmt.Sprintf("%s-%d", agentName, i)
			if _, taken := existing[candidate]; !taken {
				agentName = candidate
				break
			}
		}
	}

	claudeArgs := BuildTemplateArgs(cfg, tmpl, agentName, workspace)
	webPort := strconv.Itoa(cfg.WebPort())

	if err := m.sup.SpawnAgent(SpawnRequest{
		Name:       agentName,
		ClaudeArgs: claudeArgs,
		WorkDir:    workspace,
		Env:        tmpl.Env,
		WebPort:    webPort,
	}); err != nil {
		return Record{}, fmt.Errorf("spawning agent: %w", err)
	}

	// Persist only after a successful spawn — a Save failure does not kill the agent
	// (it's already running) but we log loudly so operators know the agent will not
	// be restored on the next daemon restart.
	if err := agentstore.Save(cfg.HomePath, agentstore.Record{
		Name:       agentName,
		Template:   spec.Template,
		Repo:       spec.Repo,
		Workspace:  workspace,
		ClaudeArgs: claudeArgs,
		Env:        tmpl.Env,
		WebPort:    webPort,
		SpawnedAt:  time.Now(),
	}); err != nil {
		log.Printf("agent %q spawned but agentstore.Save failed: %v — agent will not be restored on daemon restart", agentName, err)
	}

	return Record{
		Name:      agentName,
		Template:  spec.Template,
		Repo:      spec.Repo,
		Workspace: workspace,
		Status:    "starting",
		StartedAt: time.Now(),
		Env:       tmpl.Env,
	}, nil
}

// List returns all running ephemeral agents, merged with persisted metadata.
func (m *Manager) List() []Record {
	live := m.sup.EphemeralAgents()

	var stored map[string]agentstore.Record
	if cfg, err := m.cfgLoader(); err == nil {
		if records, err := agentstore.Load(agentstore.FilePath(cfg.HomePath)); err == nil {
			stored = records
		}
	}

	out := make([]Record, 0, len(live))
	for name, state := range live {
		r := Record{
			Name:      name,
			Status:    state.Status,
			StartedAt: state.StartedAt,
			Restarts:  state.Restarts,
		}
		mergeStored(&r, stored)
		out = append(out, r)
	}
	return out
}

// Stop kills the agent's tmux session and removes its persistence record.
func (m *Manager) Stop(name string) error {
	if err := m.sup.StopAgent(name); err != nil {
		return err
	}
	if cfg, err := m.cfgLoader(); err == nil {
		agentstore.Remove(cfg.HomePath, name)
	}
	return nil
}

// SessionName returns the tmux session name for an agent.
func (m *Manager) SessionName(name string) string {
	return "leo-" + name
}

// Logs returns the last `lines` lines of output from the agent's tmux pane.
// If lines <= 0, returns the whole scrollback.
func (m *Manager) Logs(name string, lines int) (string, error) {
	live := m.sup.EphemeralAgents()
	if _, ok := live[name]; !ok {
		return "", fmt.Errorf("agent %q not running", name)
	}

	tmuxPath := m.tmuxPath
	if tmuxPath == "" {
		found, err := exec.LookPath("tmux")
		if err != nil {
			return "", fmt.Errorf("tmux not found in PATH: %w", err)
		}
		tmuxPath = found
	}

	session := m.SessionName(name)
	args := []string{"capture-pane", "-t", session, "-p"}
	if lines > 0 {
		args = append(args, "-S", fmt.Sprintf("-%d", lines))
	} else {
		args = append(args, "-S", "-")
	}

	out, err := exec.Command(tmuxPath, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux capture-pane: %s", string(out))
	}
	return string(out), nil
}
