// Package agent owns the lifecycle of ephemeral Leo agents — template resolution,
// workspace setup, claude arg construction, supervisor registration, and persistence.
// It is consumed by the web UI, the daemon socket handlers, and the CLI, so all three
// share a single source of truth.
package agent

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/git"
)

// gitFetchTimeout bounds the single `git fetch` issued at the start of a
// worktree spawn so a flaky network can't stall the daemon indefinitely.
const gitFetchTimeout = 60 * time.Second

// maxNameReservationAttempts bounds the suffix-retry loop when a desired
// agent name is already claimed. A high cap protects against runaway loops
// without hurting the common case (one or two concurrent spawns).
const maxNameReservationAttempts = 1000

// Supervisor is the subset of service.Supervisor that the Manager needs.
// Defined here so callers inject an implementation.
//
// ReserveAgent/ReleaseAgent let the Manager atomically claim a name before
// doing slow pre-spawn work (fetch, worktree add) so concurrent spawns fail
// fast instead of racing to completion.
type Supervisor interface {
	ReserveAgent(name string) error
	ReleaseAgent(name string)
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
	Branch   string // optional — when non-empty, spawn in a dedicated worktree on this branch
	Base     string // optional — base ref for new branches (defaults to origin HEAD)
}

// Record is the public view of an agent, merging persisted metadata with live state.
// Branch + CanonicalPath are populated only for worktree agents.
type Record struct {
	Name          string            `json:"name"`
	Template      string            `json:"template,omitempty"`
	Repo          string            `json:"repo,omitempty"`
	Workspace     string            `json:"workspace,omitempty"`
	Branch        string            `json:"branch,omitempty"`
	CanonicalPath string            `json:"canonical_path,omitempty"`
	Status        string            `json:"status,omitempty"`
	StartedAt     time.Time         `json:"started_at,omitempty"`
	Restarts      int               `json:"restarts,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
}

// PruneOptions tunes Manager.Prune.
type PruneOptions struct {
	// Force removes the worktree even when dirty, and deletes the branch even
	// when it is not fully merged.
	Force bool
	// DeleteBranch removes the local branch after the worktree is gone. No-op
	// for worktrees whose branch was the repo's default.
	DeleteBranch bool
}

// Spawn resolves a template + repo into a running agent.
//
// With an empty Branch, Spawn runs today's shared-workspace flow: a single
// clone under the template workspace is reused across every agent on that
// repo.
//
// With a non-empty Branch, Spawn creates a dedicated git worktree under
// <baseWorkspace>/.worktrees/<repo-short>/<branch-slug> checked out on Branch,
// and names the agent leo-<template>-<owner>-<repo>-<branch-slug>. Fetch and
// worktree creation happen *before* the supervisor spawn — if they fail, no
// agent is registered and no record is written.
//
// The persistence write happens only after a successful supervisor spawn, so a
// failed spawn never leaves orphaned records. A failed agentstore.Save is
// logged (agent is already running and we don't want to tear it down for a
// best-effort persistence op) and results in a missing restore entry on next
// daemon start.
func (m *Manager) Spawn(ctx context.Context, spec SpawnSpec) (Record, error) {
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

	if spec.Branch != "" {
		return m.spawnWorktree(ctx, cfg, tmpl, spec)
	}
	return m.spawnShared(cfg, tmpl, spec)
}

// spawnShared is the non-worktree flow. Workspace resolution may do a network
// clone via `gh repo clone`, so we reserve the agent name first to reject
// concurrent spawns of the same name without doing the clone twice.
func (m *Manager) spawnShared(cfg *config.Config, tmpl config.TemplateConfig, spec SpawnSpec) (Record, error) {
	baseName := DeriveSharedAgentName(spec.Template, spec.Repo, spec.Name)
	agentName, err := m.reserveUniqueName(baseName)
	if err != nil {
		return Record{}, err
	}
	released := false
	release := func() {
		if !released {
			m.sup.ReleaseAgent(agentName)
			released = true
		}
	}
	defer release()

	workspace, _, err := ResolveWorkspace(tmpl, spec.Template, spec.Repo, spec.Name)
	if err != nil {
		return Record{}, err
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
	// SpawnAgent consumed the reservation on success; suppress the deferred release.
	released = true

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

// spawnWorktree implements the worktree flow. Ordering matters:
//
//  1. Reserve the agent name atomically with the supervisor so concurrent
//     spawns of the same name fail fast instead of racing through fetch and
//     worktree add.
//  2. Ensure canonical clone + compute layout (needs canonical for path).
//  3. Fetch origin.
//  4. git worktree add.
//  5. Supervisor spawn (consumes the reservation).
//  6. Persist to agentstore.
//
// Any failure before step 5 releases the reservation and, if step 4 already
// succeeded, removes the worktree so disk state stays consistent.
func (m *Manager) spawnWorktree(ctx context.Context, cfg *config.Config, tmpl config.TemplateConfig, spec SpawnSpec) (Record, error) {
	if !strings.Contains(spec.Repo, "/") {
		return Record{}, ErrWorktreeRequiresSlash
	}
	base := BaseWorkspace(tmpl)
	baseName, err := DeriveWorktreeAgentName(spec.Template, spec.Repo, spec.Branch, spec.Name)
	if err != nil {
		return Record{}, err
	}
	agentName, err := m.reserveUniqueName(baseName)
	if err != nil {
		return Record{}, err
	}
	released := false
	release := func() {
		if !released {
			m.sup.ReleaseAgent(agentName)
			released = true
		}
	}
	defer release()

	canonical, err := EnsureCanonical(base, spec.Repo)
	if err != nil {
		return Record{}, err
	}

	layout, err := ResolveWorktreeLayout(base, canonical, spec.Template, spec.Repo, spec.Branch, spec.Name)
	if err != nil {
		return Record{}, err
	}
	layout.AgentName = agentName

	fetchCtx, cancel := context.WithTimeout(ctx, gitFetchTimeout)
	defer cancel()
	if err := git.Fetch(fetchCtx, canonical); err != nil {
		return Record{}, fmt.Errorf("fetching origin: %w", err)
	}

	if err := AddWorktreeForBranch(ctx, canonical, layout.WorktreePath, layout.Branch, spec.Base); err != nil {
		return Record{}, err
	}
	worktreeCreated := true

	claudeArgs := BuildTemplateArgs(cfg, tmpl, layout.AgentName, layout.WorktreePath)
	webPort := strconv.Itoa(cfg.WebPort())

	if err := m.sup.SpawnAgent(SpawnRequest{
		Name:       layout.AgentName,
		ClaudeArgs: claudeArgs,
		WorkDir:    layout.WorktreePath,
		Env:        tmpl.Env,
		WebPort:    webPort,
	}); err != nil {
		// Reservation protected the name, so a collision here means the
		// supervisor state changed unexpectedly (e.g. concurrent restore).
		// Roll back the worktree so disk matches supervisor state.
		if worktreeCreated {
			rmCtx, rmCancel := context.WithTimeout(context.Background(), gitFetchTimeout)
			if rbErr := git.RemoveWorktree(rmCtx, canonical, layout.WorktreePath, true); rbErr != nil {
				log.Printf("spawn rollback: git worktree remove failed for %s: %v", layout.WorktreePath, rbErr)
			}
			rmCancel()
		}
		return Record{}, fmt.Errorf("spawning agent: %w", err)
	}
	// SpawnAgent consumed the reservation on success.
	released = true

	if err := agentstore.Save(cfg.HomePath, agentstore.Record{
		Name:          layout.AgentName,
		Template:      spec.Template,
		Repo:          spec.Repo,
		Workspace:     layout.WorktreePath,
		Branch:        layout.Branch,
		CanonicalPath: canonical,
		ClaudeArgs:    claudeArgs,
		Env:           tmpl.Env,
		WebPort:       webPort,
		SpawnedAt:     time.Now(),
	}); err != nil {
		log.Printf("agent %q spawned but agentstore.Save failed: %v — agent will not be restored on daemon restart", layout.AgentName, err)
	}

	return Record{
		Name:          layout.AgentName,
		Template:      spec.Template,
		Repo:          spec.Repo,
		Workspace:     layout.WorktreePath,
		Branch:        layout.Branch,
		CanonicalPath: canonical,
		Status:        "starting",
		StartedAt:     time.Now(),
		Env:           tmpl.Env,
	}, nil
}

// reserveUniqueName atomically reserves the first available variant of name
// with the supervisor, appending -2, -3, ... on collision. The caller owns
// the returned reservation and must either pass it to SpawnAgent (which
// consumes it) or call sup.ReleaseAgent on failure.
func (m *Manager) reserveUniqueName(name string) (string, error) {
	if err := m.sup.ReserveAgent(name); err == nil {
		return name, nil
	}
	for i := 2; i < maxNameReservationAttempts; i++ {
		candidate := fmt.Sprintf("%s-%d", name, i)
		if err := m.sup.ReserveAgent(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not reserve a unique name for %q after %d attempts", name, maxNameReservationAttempts)
}

// List returns ephemeral agents merged with persisted metadata. Running agents
// always appear; stopped worktree agents also appear (with status "stopped")
// so operators can see candidates for pruning. Shared-workspace agents drop
// out of the list once stopped, matching pre-worktree behavior.
func (m *Manager) List() []Record {
	live := m.sup.EphemeralAgents()

	var stored map[string]agentstore.Record
	if cfg, err := m.cfgLoader(); err == nil {
		if records, err := agentstore.Load(agentstore.FilePath(cfg.HomePath)); err == nil {
			stored = records
		}
	}

	out := make([]Record, 0, len(live)+len(stored))
	seen := make(map[string]struct{}, len(live))

	for name, state := range live {
		r := Record{
			Name:      name,
			Status:    state.Status,
			StartedAt: state.StartedAt,
			Restarts:  state.Restarts,
		}
		mergeStored(&r, stored)
		out = append(out, r)
		seen[name] = struct{}{}
	}

	for name, rec := range stored {
		if _, alive := seen[name]; alive {
			continue
		}
		if rec.Branch == "" {
			continue
		}
		out = append(out, Record{
			Name:          name,
			Template:      rec.Template,
			Repo:          rec.Repo,
			Workspace:     rec.Workspace,
			Branch:        rec.Branch,
			CanonicalPath: rec.CanonicalPath,
			Status:        "stopped",
			StartedAt:     rec.SpawnedAt,
			Env:           rec.Env,
		})
	}
	return out
}

// Stop kills the agent's tmux session. For shared-workspace agents the
// agentstore record is also removed (nothing to clean up later). For worktree
// agents the record is preserved so Prune can find it; operators can always
// call Prune explicitly to drop the worktree and record in one step.
func (m *Manager) Stop(name string) error {
	if err := m.sup.StopAgent(name); err != nil {
		return err
	}
	cfg, err := m.cfgLoader()
	if err != nil {
		return nil
	}
	stored, err := agentstore.Load(agentstore.FilePath(cfg.HomePath))
	if err != nil {
		// Best-effort: a missing file means nothing to remove. A parse
		// error leaves the record in place; Prune will surface it.
		return nil
	}
	if rec, ok := stored[name]; ok && rec.Branch != "" {
		return nil
	}
	agentstore.Remove(cfg.HomePath, name)
	return nil
}

// Prune removes the on-disk worktree and agentstore record for a stopped
// worktree agent. Returns ErrAgentStillRunning if the agent has a live tmux
// session, ErrNotWorktreeAgent for shared-workspace agents, and
// ErrWorktreeDirty / ErrBranchNotMerged from the git layer when --force is
// required to proceed.
func (m *Manager) Prune(ctx context.Context, name string, opts PruneOptions) error {
	cfg, err := m.cfgLoader()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	stored, err := agentstore.Load(agentstore.FilePath(cfg.HomePath))
	if err != nil {
		return fmt.Errorf("loading agentstore: %w", err)
	}
	rec, ok := stored[name]
	if !ok {
		return fmt.Errorf("no agentstore record for %q", name)
	}
	if rec.Branch == "" || rec.CanonicalPath == "" {
		return ErrNotWorktreeAgent
	}

	if live := m.sup.EphemeralAgents(); live != nil {
		if _, alive := live[name]; alive {
			return ErrAgentStillRunning
		}
	}

	if err := git.RemoveWorktree(ctx, rec.CanonicalPath, rec.Workspace, opts.Force); err != nil {
		return err
	}

	if opts.DeleteBranch {
		if err := git.DeleteBranch(ctx, rec.CanonicalPath, rec.Branch, opts.Force); err != nil {
			return err
		}
	}

	agentstore.Remove(cfg.HomePath, name)
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
