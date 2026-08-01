// Package agent owns the lifecycle of ephemeral Leo agents — template resolution,
// workspace setup, claude arg construction, supervisor registration, and persistence.
// It is consumed by the web UI, the daemon socket handlers, and the CLI, so all three
// share a single source of truth.
package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/git"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/observe"
	"github.com/blackpaw-studio/leo/internal/session"
	"github.com/blackpaw-studio/leo/internal/tmux"
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
	// SuspendAgent stops name's process/tmux session exactly like StopAgent,
	// but announces the transition as observe.EventAgentStateChanged with
	// status "suspended" rather than observe.EventAgentStopped — a suspended
	// agent is coming back (see SpawnRequest.Resumed), not gone, and a
	// consumer needs to be able to tell those apart.
	SuspendAgent(name string) error
	RenameAgent(old, new string) error
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
	// webToken is the daemon's API bearer token, propagated into every
	// SpawnRequest so the supervisor can export LEO_API_TOKEN for the
	// agent's MCP server.
	webToken string
	// publisher announces the lifecycle transitions Manager itself decides
	// never to forward to sup (a stopped/suspended agent has no live process
	// for sup.StopAgent/RenameAgent to act on, so those calls — and their
	// publishes — are skipped entirely on that path; see Stop and Rename).
	// Optional: nil (the default) makes publish a safe no-op, matching
	// service.Supervisor's own publisher seam.
	publisher observe.Publisher
}

// SetPublisher wires an observe.Publisher into the Manager, for lifecycle
// transitions that happen purely against the agentstore (no live sup call).
// Optional; daemon boot is the only production caller.
func (m *Manager) SetPublisher(p observe.Publisher) {
	m.publisher = p
}

// publish is a nil-safe no-op when no publisher has been configured.
func (m *Manager) publish(ev observe.Event) {
	if m.publisher == nil {
		return
	}
	m.publisher.Publish(ev)
}

// New constructs a Manager. tmuxPath is used for Logs (tmux capture-pane); pass the
// empty string to have the Manager look up tmux from $PATH on demand. webToken is
// the daemon's API bearer token; pass the empty string to leave LEO_API_TOKEN unset
// (the MCP server will fail fast, matching the "web auth required" invariant).
func New(cfgLoader ConfigLoader, sup Supervisor, tmuxPath, webToken string) *Manager {
	return &Manager{cfgLoader: cfgLoader, sup: sup, tmuxPath: tmuxPath, webToken: webToken}
}

// SpawnSpec describes a spawn request in terms of high-level intent.
type SpawnSpec struct {
	Template string // required — template name from config.Templates
	// Repo is optional. owner/repo clones into the template workspace; a bare
	// name reuses the template workspace under a per-name subdir; empty runs
	// the template as-is directly in its own workspace (agent named after the
	// template). Branch (worktree mode) requires a non-empty owner/repo.
	Repo   string
	Name   string // optional — overrides the derived agent name
	Branch string // optional — when non-empty, spawn in a dedicated worktree on this branch
	Base   string // optional — base ref for new branches (defaults to origin HEAD)
	// FromAgent, when non-empty, derives the spawn from an existing agent's
	// record: template and env are inherited, and the source workspace (or
	// canonical, for worktree sources) becomes the git canonical. Requires
	// Branch; Template and Repo must be empty. Works for any agent whose
	// workspace is a git repository — no owner/repo needed.
	FromAgent string
	// Prompt, when non-empty, is delivered to the agent as the opening turn of
	// its interactive session (appended as the trailing positional claude arg).
	Prompt string
	// Env is merged over the template's env for this spawn only. Per-spawn keys
	// win on collision. Lets a caller hand the agent context like SLACK_THREAD_TS.
	Env map[string]string
	// IdleSuspend, when non-empty, overrides the template/defaults
	// idle_suspend_after for this spawn only (a Go duration like "24h").
	IdleSuspend string
}

// mergeEnv returns a new map combining base and overlay, with overlay winning
// on key collision. Neither input is mutated. Returns nil when both are empty
// so callers preserve the "no env" representation exactly.
func mergeEnv(base, overlay map[string]string) map[string]string {
	if len(base) == 0 && len(overlay) == 0 {
		return nil
	}
	merged := make(map[string]string, len(base)+len(overlay))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range overlay {
		merged[k] = v
	}
	return merged
}

// pruneEnv returns a copy of env with any key also defined in fresh removed.
// Used to keep a stale inherited-env layer (a worktree/from-agent spawn's
// source-agent env, or the same layer replayed at restart) from shadowing a
// key the harness's own env now defines — same semantics whether "fresh" is
// the harness env computed at spawn time or recomputed at restart time.
func pruneEnv(env, fresh map[string]string) map[string]string {
	if len(env) == 0 || len(fresh) == 0 {
		return env
	}
	pruned := make(map[string]string, len(env))
	for k, v := range env {
		if _, shadowed := fresh[k]; !shadowed {
			pruned[k] = v
		}
	}
	return pruned
}

// Record is the public view of an agent, merging persisted metadata with live state.
// Branch + CanonicalPath are populated only for worktree agents.
//
// Deliberately carries no env: this struct is the payload of GET
// /api/agent/list, which the leo_list_agents MCP tool hands to any agent that
// asks. Agent env holds live credentials, and a listing call must not deposit
// them into a transcript. The supervisor reads env from the agentstore record
// (agentstore.Record.Env), which never crosses that boundary.
type Record struct {
	Name          string    `json:"name"`
	Template      string    `json:"template,omitempty"`
	Repo          string    `json:"repo,omitempty"`
	Workspace     string    `json:"workspace,omitempty"`
	Branch        string    `json:"branch,omitempty"`
	CanonicalPath string    `json:"canonical_path,omitempty"`
	Status        string    `json:"status,omitempty"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	Restarts      int       `json:"restarts,omitempty"`
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
	cfg, err := m.cfgLoader()
	if err != nil {
		return Record{}, fmt.Errorf("loading config: %w", err)
	}
	if spec.FromAgent != "" {
		if spec.Repo != "" {
			return Record{}, fmt.Errorf("from-agent spawn derives the repo from the source agent; do not set it")
		}
		return m.spawnFromAgent(ctx, cfg, spec)
	}
	if spec.Template == "" {
		return Record{}, fmt.Errorf("template is required")
	}
	tmpl, ok := cfg.Templates[spec.Template]
	if !ok {
		return Record{}, fmt.Errorf("template %q not found", spec.Template)
	}
	return m.spawnResolved(ctx, cfg, tmpl, spec)
}

// SpawnFromTemplate spawns a repo-less agent named `name` directly from an
// already-resolved TemplateConfig, skipping the cfg.Templates[...] lookup
// Spawn performs. Used by the daemon's ensure-exists task-delivery path
// (internal/daemon/ensure.go) for persistent tasks with an implicit target —
// config.ResolveTaskTarget synthesizes tmpl from the task's own fields rather
// than a named config template, so there is nothing to look up.
func (m *Manager) SpawnFromTemplate(ctx context.Context, name string, tmpl config.TemplateConfig) (Record, error) {
	if name == "" {
		return Record{}, fmt.Errorf("name is required")
	}
	cfg, err := m.cfgLoader()
	if err != nil {
		return Record{}, fmt.Errorf("loading config: %w", err)
	}
	return m.spawnResolved(ctx, cfg, tmpl, SpawnSpec{Template: name, Name: name})
}

// spawnResolved is the shared post-template-lookup body for Spawn and
// SpawnFromTemplate: validate, then route to the worktree or shared-workspace
// flow.
func (m *Manager) spawnResolved(ctx context.Context, cfg *config.Config, tmpl config.TemplateConfig, spec SpawnSpec) (Record, error) {
	if spec.Branch != "" && spec.Repo == "" {
		return Record{}, fmt.Errorf("--worktree requires a repo")
	}
	if spec.Repo != "" {
		if err := ValidateRepo(spec.Repo); err != nil {
			return Record{}, err
		}
	}

	if spec.Branch != "" {
		return m.spawnWorktree(ctx, cfg, tmpl, spec)
	}
	return m.spawnShared(cfg, tmpl, spec)
}

// Live reports whether name is a currently running (supervised) agent.
// Live reports whether name is a currently supervised ephemeral agent whose
// process is up or coming up. "running" and "starting" both count: the
// injector readiness-probes before delivering, so it's safe (and desirable)
// to treat a still-booting agent as a valid injection target rather than
// falsely reporting "not live" and triggering a redundant spawn/resume.
// "restarting" and "stopped" do not count — there is no process to inject
// into yet.
func (m *Manager) Live(name string) bool {
	info, ok := m.sup.EphemeralAgents()[name]
	if !ok {
		return false
	}
	return info.Status == "running" || info.Status == "starting"
}

// Suspended reports whether name has a persisted agentstore record marked
// Suspended (and is not currently live). Config/store load failures are
// treated as "not suspended" — the caller (the ensure-exists path) falls
// through to spawning fresh, which is the safe default when state can't be
// read.
func (m *Manager) Suspended(name string) bool {
	if m.Live(name) {
		return false
	}
	cfg, err := m.cfgLoader()
	if err != nil {
		return false
	}
	stored, err := agentstore.Load(agentstore.FilePath(cfg.HomePath))
	if err != nil {
		return false
	}
	rec, ok := stored[name]
	return ok && rec.Suspended
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

	harnessName := cfg.TemplateHarness(tmpl)
	isClaude := harnessName == "" || harnessName == "claude"

	sessionID := session.NewID()
	claudeArgs, harnessEnv := BuildTemplateArgs(cfg, tmpl, agentName, workspace, spec.Prompt, m.webToken)
	openingPrompt := ""
	// storedSessionID seeds agentstore.Record.SessionID, which the tmux-TUI
	// driver's SessionIDStore (agent.NewAgentIDs) reads back via IDs.Get() to
	// decide whether this is a brand-new session (Start's opening-prompt
	// precondition is Get()=="") or a resume. Claude's sessionID is a
	// leo-generated --session-id it hands claude on the command line, so it
	// is genuinely "already assigned" from claude's perspective — storing it
	// is correct. A non-claude harness has never talked to its own rollout/
	// session yet at this point (the driver discovers that id post-hoc, after
	// the opening prompt is injected and the first turn runs), so seeding
	// this field with the same leo-generated uuid would make IDs.Get()
	// falsely non-empty and skip the opening-prompt injection entirely.
	storedSessionID := ""
	if isClaude {
		claudeArgs = append(claudeArgs, "--session-id", sessionID)
		storedSessionID = sessionID
	} else {
		openingPrompt = spec.Prompt
	}
	webPort := strconv.Itoa(cfg.WebPort())
	env := mergeEnv(mergeEnv(harnessEnv, tmpl.Env), spec.Env)

	idleStr := ""
	if d := cfg.ResolveIdleSuspend(tmpl, spec.IdleSuspend); d > 0 {
		idleStr = d.String()
	}

	// The agentstore record is persisted BEFORE the supervisor spawn (not
	// after, as the claude-only flow historically did). A non-claude
	// harness's tmux-TUI driver injects the opening prompt and starts
	// post-hoc session-id discovery from inside the supervise goroutine
	// almost immediately after spawn, and that discovery's SessionIDStore
	// (agentOrProcessIDs) picks agentstore-backed persistence only when a
	// record already exists for this name — save-after-spawn lost that race
	// essentially every time, silently stashing the freshly discovered id
	// under a throwaway "process:<name>" session-store key instead of the
	// agent's own record, which then made every subsequent restart think it
	// had no prior session and start over. Saving first closes that window.
	// A failed spawn now rolls the record back so a dead agent never leaves
	// an orphaned entry behind (matches the pre-existing worktree-flow
	// rollback further down this file).
	if err := agentstore.Save(cfg.HomePath, agentstore.Record{
		Name:             agentName,
		Template:         spec.Template,
		Repo:             spec.Repo,
		Workspace:        workspace,
		ClaudeArgs:       claudeArgs,
		SessionID:        storedSessionID,
		Env:              env,
		SpawnEnv:         spec.Env,
		WebPort:          webPort,
		SpawnedAt:        time.Now(),
		IdleSuspendAfter: idleStr,
		Harness:          harnessName,
	}); err != nil {
		log.Printf("agent %q: agentstore.Save failed before spawn: %v — agent will not be restored on daemon restart", agentName, err)
	}

	if err := m.sup.SpawnAgent(SpawnRequest{
		Name:          agentName,
		ClaudeArgs:    claudeArgs,
		WorkDir:       workspace,
		Env:           env,
		WebPort:       webPort,
		WebToken:      m.webToken,
		Harness:       harnessName,
		OpeningPrompt: openingPrompt,
	}); err != nil {
		agentstore.Remove(cfg.HomePath, agentName)
		return Record{}, fmt.Errorf("spawning agent: %w", err)
	}
	// SpawnAgent consumed the reservation on success; suppress the deferred release.
	released = true

	return Record{
		Name:      agentName,
		Template:  spec.Template,
		Repo:      spec.Repo,
		Workspace: workspace,
		Status:    "starting",
		StartedAt: time.Now(),
	}, nil
}

// worktreeSpawnParams carries what spawnWorktreeCore needs beyond the
// SpawnSpec: how to obtain the canonical repo, how to lay out the worktree,
// and what to persist/inherit. Two producers exist — spawnWorktree
// (owner/repo mode) and spawnFromAgent (existing-agent mode).
type worktreeSpawnParams struct {
	// baseName is the derived agent name before -2/-3 collision suffixing.
	baseName string
	// canonical returns the canonical repo path. Called after the agent name
	// is reserved so slow work (cloning) never races a concurrent spawn.
	canonical func() (string, error)
	// layout computes the worktree layout given the canonical path.
	layout func(canonical string) (WorktreeLayout, error)
	// fetch controls whether origin is fetched before the worktree add.
	// False when the canonical has no origin remote.
	fetch bool
	// repo is persisted as the record's Repo. May be empty or a bare name
	// for from-agent spawns.
	repo string
	// inheritEnv is merged between tmpl.Env and spec.Env, minus any key the
	// freshly computed harness env defines — stale harness values from a
	// source agent's record must not leak into the new agent.
	inheritEnv map[string]string
}

func (m *Manager) spawnWorktree(ctx context.Context, cfg *config.Config, tmpl config.TemplateConfig, spec SpawnSpec) (Record, error) {
	if !strings.Contains(spec.Repo, "/") {
		return Record{}, ErrWorktreeRequiresSlash
	}
	base := BaseWorkspace(tmpl)
	baseName, err := DeriveWorktreeAgentName(spec.Template, spec.Repo, spec.Branch, spec.Name)
	if err != nil {
		return Record{}, err
	}
	return m.spawnWorktreeCore(ctx, cfg, tmpl, spec, worktreeSpawnParams{
		baseName:  baseName,
		canonical: func() (string, error) { return EnsureCanonical(base, spec.Repo) },
		layout: func(canonical string) (WorktreeLayout, error) {
			return ResolveWorktreeLayout(base, canonical, spec.Template, spec.Repo, spec.Branch, spec.Name)
		},
		fetch: true,
		repo:  spec.Repo,
	})
}

// lookupStored returns the raw agentstore record for query, trying the query
// verbatim and its normalized form. Mirrors resolveStored but returns the
// stored record (env, canonical path) rather than the public Record view.
func lookupStored(homePath, query string) (agentstore.Record, bool) {
	stored, err := agentstore.Load(agentstore.FilePath(homePath))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return agentstore.Record{}, false
	}
	candidates := []string{strings.TrimSpace(query)}
	if norm, err := NormalizeAgentName(query); err == nil {
		candidates = append(candidates, norm)
	}
	for _, name := range candidates {
		if rec, ok := stored[name]; ok {
			if rec.Name == "" {
				rec.Name = name
			}
			return rec, true
		}
	}
	return agentstore.Record{}, false
}

// spawnFromAgent spawns a worktree agent derived from an existing agent's
// record. Unlike spawnWorktree there is no owner/repo requirement: the
// source agent's workspace (or its canonical, for worktree sources) is the
// git canonical, fetch is skipped for remoteless repos, and new branches on
// remoteless repos fall back to HEAD as their base ref.
func (m *Manager) spawnFromAgent(ctx context.Context, cfg *config.Config, spec SpawnSpec) (Record, error) {
	if spec.Branch == "" {
		return Record{}, fmt.Errorf("worktree spawn requires a branch name")
	}
	src, ok := lookupStored(cfg.HomePath, spec.FromAgent)
	if !ok {
		return Record{}, fmt.Errorf("%w: %q (run 'leo agent list')", ErrSourceAgentNotFound, spec.FromAgent)
	}
	srcTmpl, ok := cfg.Templates[src.Template]
	if !ok {
		return Record{}, fmt.Errorf("template %q (from agent %q) not found", src.Template, src.Name)
	}
	canonical := src.CanonicalPath
	if canonical == "" {
		canonical = src.Workspace
	}
	if _, err := os.Stat(filepath.Join(canonical, ".git")); err != nil {
		return Record{}, fmt.Errorf("%w: %s", ErrSourceNotGitRepo, canonical)
	}
	hasOrigin := git.HasOrigin(ctx, canonical)
	if !hasOrigin && spec.Base == "" {
		// AddWorktreeForBranch resolves origin's default branch when no base
		// is given; remoteless repos have no origin, so branch off HEAD.
		spec.Base = "HEAD"
	}

	// runTmpl is the template that actually builds the new agent: the source
	// template by default, or the caller's --template override. The base
	// workspace (worktree placement) always stays derived from the SOURCE
	// template so an override with a different pinned workspace doesn't
	// relocate the worktree away from the source agent's other worktrees.
	runTmpl := srcTmpl
	runTmplName := src.Template
	inheritEnv := src.Env
	if spec.Template != "" {
		t, ok := cfg.Templates[spec.Template]
		if !ok {
			return Record{}, fmt.Errorf("template %q not found", spec.Template)
		}
		runTmpl = t
		runTmplName = spec.Template
		inheritEnv = nil // override template: do not inherit the source agent's env
	}
	spec.Template = runTmplName

	base := BaseWorkspace(srcTmpl)
	layout, err := ResolveAgentWorktreeLayout(base, canonical, src.Name, spec.Branch, spec.Name)
	if err != nil {
		return Record{}, err
	}
	return m.spawnWorktreeCore(ctx, cfg, runTmpl, spec, worktreeSpawnParams{
		baseName:  layout.AgentName,
		canonical: func() (string, error) { return canonical, nil },
		layout: func(string) (WorktreeLayout, error) {
			return ResolveAgentWorktreeLayout(base, canonical, src.Name, spec.Branch, spec.Name)
		},
		fetch:      hasOrigin,
		repo:       src.Repo,
		inheritEnv: inheritEnv,
	})
}

// spawnWorktreeCore implements the worktree flow. Ordering matters:
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
func (m *Manager) spawnWorktreeCore(ctx context.Context, cfg *config.Config, tmpl config.TemplateConfig, spec SpawnSpec, p worktreeSpawnParams) (Record, error) {
	agentName, err := m.reserveUniqueName(p.baseName)
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

	canonical, err := p.canonical()
	if err != nil {
		return Record{}, err
	}

	layout, err := p.layout(canonical)
	if err != nil {
		return Record{}, err
	}
	layout.AgentName = agentName

	if p.fetch {
		fetchCtx, cancel := context.WithTimeout(ctx, gitFetchTimeout)
		defer cancel()
		if err := git.Fetch(fetchCtx, canonical); err != nil {
			return Record{}, fmt.Errorf("fetching origin: %w", err)
		}
	}

	if err := AddWorktreeForBranch(ctx, canonical, layout.WorktreePath, layout.Branch, spec.Base); err != nil {
		return Record{}, err
	}
	worktreeCreated := true

	harnessName := cfg.TemplateHarness(tmpl)
	isClaude := harnessName == "" || harnessName == "claude"

	sessionID := session.NewID()
	claudeArgs, harnessEnv := BuildTemplateArgs(cfg, tmpl, layout.AgentName, layout.WorktreePath, spec.Prompt, m.webToken)
	openingPrompt := ""
	// See the identical storedSessionID comment in spawnShared: a non-claude
	// harness must NOT have its agentstore SessionID pre-seeded, or its
	// tmux-TUI driver's opening-prompt precondition (IDs.Get()=="") is
	// falsely non-empty and the opening prompt never gets injected.
	storedSessionID := ""
	if isClaude {
		claudeArgs = append(claudeArgs, "--session-id", sessionID)
		storedSessionID = sessionID
	} else {
		openingPrompt = spec.Prompt
	}
	webPort := strconv.Itoa(cfg.WebPort())
	// p.inheritEnv is stored on the record RAW (unpruned) as InheritedEnv, so
	// a config-aware restart can re-prune it against the harness env computed
	// at restart time instead of replaying this spawn-time snapshot — a
	// harness env key that didn't exist yet at spawn time must still be able
	// to win on restart. spec.Env is stored separately as SpawnEnv: explicit
	// --env overrides always win, including over harness env, matching
	// spawnShared's layering (mergeEnv(harnessEnv, tmpl.Env) as the base,
	// caller env as the top overlay).
	inherited := pruneEnv(p.inheritEnv, harnessEnv)
	env := mergeEnv(mergeEnv(mergeEnv(harnessEnv, tmpl.Env), inherited), spec.Env)

	// rollbackWorktree removes the worktree created above so disk state stays
	// consistent with the supervisor whenever a step after worktree creation
	// fails.
	rollbackWorktree := func() {
		if !worktreeCreated {
			return
		}
		rmCtx, rmCancel := context.WithTimeout(context.Background(), gitFetchTimeout)
		if rbErr := git.RemoveWorktree(rmCtx, canonical, layout.WorktreePath, true); rbErr != nil {
			log.Printf("spawn rollback: git worktree remove failed for %s: %v", layout.WorktreePath, rbErr)
		}
		rmCancel()
	}

	idleStr := ""
	if d := cfg.ResolveIdleSuspend(tmpl, spec.IdleSuspend); d > 0 {
		idleStr = d.String()
	}

	// Persist before spawning — see the identical comment in spawnShared for
	// why order matters here (a non-claude harness's tmux-TUI driver injects
	// the opening prompt and starts session-id discovery, both reading back
	// the just-saved record's SessionIDStore, from inside the supervise
	// goroutine almost immediately after spawn).
	if err := agentstore.Save(cfg.HomePath, agentstore.Record{
		Name:             layout.AgentName,
		Template:         spec.Template,
		Repo:             p.repo,
		Workspace:        layout.WorktreePath,
		Branch:           layout.Branch,
		CanonicalPath:    canonical,
		ClaudeArgs:       claudeArgs,
		SessionID:        storedSessionID,
		Env:              env,
		SpawnEnv:         spec.Env,
		InheritedEnv:     p.inheritEnv,
		WebPort:          webPort,
		SpawnedAt:        time.Now(),
		IdleSuspendAfter: idleStr,
		Harness:          harnessName,
	}); err != nil {
		log.Printf("agent %q: agentstore.Save failed before spawn: %v — agent will not be restored on daemon restart", layout.AgentName, err)
	}

	if err := m.sup.SpawnAgent(SpawnRequest{
		Name:          layout.AgentName,
		ClaudeArgs:    claudeArgs,
		WorkDir:       layout.WorktreePath,
		Env:           env,
		WebPort:       webPort,
		WebToken:      m.webToken,
		Harness:       harnessName,
		OpeningPrompt: openingPrompt,
	}); err != nil {
		// Reservation protected the name, so a collision here means the
		// supervisor state changed unexpectedly (e.g. concurrent restore).
		// Roll back the worktree AND the just-written record so disk matches
		// supervisor state.
		rollbackWorktree()
		agentstore.Remove(cfg.HomePath, layout.AgentName)
		return Record{}, fmt.Errorf("spawning agent: %w", err)
	}
	// SpawnAgent consumed the reservation on success.
	released = true

	return Record{
		Name:          layout.AgentName,
		Template:      spec.Template,
		Repo:          p.repo,
		Workspace:     layout.WorktreePath,
		Branch:        layout.Branch,
		CanonicalPath: canonical,
		Status:        "starting",
		StartedAt:     time.Now(),
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
		if rec.Suspended {
			out = append(out, Record{
				Name:          name,
				Template:      rec.Template,
				Repo:          rec.Repo,
				Workspace:     rec.Workspace,
				Branch:        rec.Branch,
				CanonicalPath: rec.CanonicalPath,
				Status:        "suspended",
				StartedAt:     rec.SpawnedAt,
			})
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
		})
	}
	// Sort by name for a stable order: `out` is assembled by ranging over the
	// live and stored maps, whose iteration order is randomized, so without this
	// the list reshuffles on every refresh/suspend/resume.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Stop kills the agent's tmux session. For shared-workspace agents the
// agentstore record is also removed (nothing to clean up later). For worktree
// agents the record is preserved with Stopped=true so Prune can find the
// checkout while RestoreAgents knows not to resurrect the agent on daemon
// restart; operators can always call Prune explicitly to drop the worktree
// and record in one step.
//
// Stop also terminates a suspended agent: a suspended (or otherwise dead) agent
// has no supervised process, so StopAgent would return "not found". We only
// call StopAgent when the agent is actually live and otherwise fall straight
// through to record cleanup, clearing Suspended so the agent shows as stopped
// and never auto-resumes. A name that is neither live nor persisted is a real
// "not found".
func (m *Manager) Stop(name string) error {
	_, live := m.sup.EphemeralAgents()[name]
	if live {
		if err := m.sup.StopAgent(name); err != nil {
			return err
		}
	}

	// Config/store failures are swallowed only when the process was live: it's
	// already been killed, so cleanup is best-effort (a parse error leaves the
	// record for Prune to surface). When the agent is NOT live, the record
	// lookup is the whole operation — a failure there must be reported, not
	// silently treated as success.
	cfg, err := m.cfgLoader()
	if err != nil {
		if live {
			return nil
		}
		return fmt.Errorf("loading config to stop agent %q: %w", name, err)
	}
	stored, err := agentstore.Load(agentstore.FilePath(cfg.HomePath))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		if live {
			return nil
		}
		return fmt.Errorf("loading agentstore to stop agent %q: %w", name, err)
	}
	rec, ok := stored[name]
	if !ok {
		if !live {
			// Nothing to stop: not running and never persisted (or the store
			// file is missing, which stored=nil already reflects).
			return fmt.Errorf("agent %q not found", name)
		}
		// Live but record-less (a legitimate shared, unpersisted agent) — the
		// process is already killed; nothing left to clean up.
		agentstore.Remove(cfg.HomePath, name)
		return nil
	}
	if rec.Branch != "" {
		rec.Stopped = true
		rec.Suspended = false
		if saveErr := agentstore.Save(cfg.HomePath, rec); saveErr != nil {
			log.Printf("agent %q stopped but marking Stopped=true failed: %v — agent may be resurrected on daemon restart", name, saveErr)
		}
		m.announceStoppedIfNotLive(live, name)
		return nil
	}
	agentstore.Remove(cfg.HomePath, name)
	m.announceStoppedIfNotLive(live, name)
	return nil
}

// announceStoppedIfNotLive publishes agent_stopped for a Stop that was
// satisfied purely against the agentstore (the agent was suspended or
// otherwise not live, so sup.StopAgent — and the publish inside it — was
// never called; see Stop's doc comment). When live is true, sup.StopAgent
// already published this event, so this is a deliberate no-op to avoid a
// duplicate.
func (m *Manager) announceStoppedIfNotLive(live bool, name string) {
	if live {
		return
	}
	m.publish(observe.Event{
		Type:    observe.EventAgentStopped,
		Payload: &observe.AgentStoppedPayload{Agent: name},
	})
}

// Suspend stops a running ephemeral agent's process and tmux session while
// preserving its agentstore record (Suspended=true) and SessionID, so the
// conversation auto-resumes on the next incoming message. The record is marked
// before the process is stopped; a failed stop rolls the flag back so the
// record never claims "suspended" while the process is still running. Returns
// an error when the agent is not currently running or has no persisted record.
func (m *Manager) Suspend(name string) error {
	if _, ok := m.sup.EphemeralAgents()[name]; !ok {
		return fmt.Errorf("agent %q is not running", name)
	}
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
		return fmt.Errorf("no agentstore record for %q (cannot suspend an unpersisted agent)", name)
	}

	rec.Suspended = true
	rec.NoResume = false
	if err := agentstore.Save(cfg.HomePath, rec); err != nil {
		return fmt.Errorf("marking agent suspended: %w", err)
	}

	if err := m.sup.SuspendAgent(name); err != nil {
		rec.Suspended = false
		if rbErr := agentstore.Save(cfg.HomePath, rec); rbErr != nil {
			log.Printf("agent %q: stop failed (%v) AND suspend-flag rollback failed (%v)", name, err, rbErr)
		}
		return fmt.Errorf("stopping agent for suspend: %w", err)
	}
	return nil
}

// Resume restarts a suspended ephemeral agent, rejoining its prior claude
// session via --resume. If the agent is already running it is a no-op that
// returns the live record. Errors when name has no suspended record.
//
// Mirrors RestoreAgents' resume logic: prefer the newest jsonl in the
// workspace over the stored SessionID (catches /clear sessions the store never
// saw), then spawn with ResumeArgs and clear the Suspended flag. The stored
// ClaudeArgs are left untouched (still carrying --session-id); a future restore
// rebuilds resume args from them + the SessionID, matching existing behavior.
func (m *Manager) Resume(name string) (Record, error) {
	if st, ok := m.sup.EphemeralAgents()[name]; ok {
		r := Record{Name: name, Status: st.Status, StartedAt: st.StartedAt, Restarts: st.Restarts}
		if cfg, err := m.cfgLoader(); err == nil {
			if stored, err := agentstore.Load(agentstore.FilePath(cfg.HomePath)); err == nil {
				mergeStored(&r, stored)
			}
		}
		return r, nil
	}

	cfg, err := m.cfgLoader()
	if err != nil {
		return Record{}, fmt.Errorf("loading config: %w", err)
	}
	stored, err := agentstore.Load(agentstore.FilePath(cfg.HomePath))
	if err != nil {
		return Record{}, fmt.Errorf("loading agentstore: %w", err)
	}
	rec, ok := stored[name]
	if !ok || !rec.Suspended {
		return Record{}, fmt.Errorf("agent %q is not suspended", name)
	}

	// The jsonl scan is a claude-specific resume mechanic (claude's own
	// on-disk session transcripts); non-claude records resume with their
	// stored args/SessionID unchanged.
	resumeID := rec.SessionID
	isClaude := rec.Harness == "" || rec.Harness == "claude"
	if isClaude {
		if latestID, _, err := session.LatestSession(rec.Workspace, 0); err == nil && latestID != "" {
			resumeID = latestID
		}
	}
	args := rec.ClaudeArgs
	if isClaude {
		args = ResumeArgs(rec.ClaudeArgs, resumeID)
	}

	if err := m.sup.SpawnAgent(SpawnRequest{
		Name:       rec.Name,
		ClaudeArgs: args,
		WorkDir:    rec.Workspace,
		Env:        rec.Env,
		WebPort:    rec.WebPort,
		WebToken:   m.webToken,
		Harness:    rec.Harness,
		Resumed:    true,
	}); err != nil {
		return Record{}, fmt.Errorf("respawning suspended agent: %w", err)
	}

	rec.Suspended = false
	rec.SessionID = resumeID
	if err := agentstore.Save(cfg.HomePath, rec); err != nil {
		log.Printf("agent %q resumed but agentstore.Save failed: %v — flag may persist until next save", rec.Name, err)
	}

	return Record{
		Name:          rec.Name,
		Template:      rec.Template,
		Repo:          rec.Repo,
		Workspace:     rec.Workspace,
		Branch:        rec.Branch,
		CanonicalPath: rec.CanonicalPath,
		Status:        "starting",
		StartedAt:     time.Now(),
	}, nil
}

// Reset forces an agent back to a brand-new conversation: it stops any live
// process/tmux session, clears the persisted SessionID (and marks NoResume so
// a daemon restart racing this call can't resurrect the old session via
// RestoreAgents' jsonl scan), then respawns the agent fresh — the same
// spawn-a-new-session logic Spawn/spawnShared uses, not Resume's --resume
// path. Errors when name has no persisted agentstore record. If the respawn
// itself fails, the record is left in that already-cleared interim state
// (SessionID="", NoResume=true, Suspended=false) rather than rolled back;
// re-running Reset on the same name recovers, since the agent is no longer
// live so the stop is skipped and only the spawn is retried.
func (m *Manager) Reset(name string) error {
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
		return fmt.Errorf("no agentstore record for %q (cannot reset an unpersisted agent)", name)
	}

	if _, live := m.sup.EphemeralAgents()[name]; live {
		if err := m.sup.StopAgent(name); err != nil {
			return fmt.Errorf("stopping agent for reset: %w", err)
		}
	}

	rec.SessionID = ""
	rec.NoResume = true
	rec.Suspended = false
	if err := agentstore.Save(cfg.HomePath, rec); err != nil {
		return fmt.Errorf("clearing agent session state: %w", err)
	}

	// Fresh spawn, not resume: strip any stored --session-id/--resume flags,
	// then (claude only) mint and pass a brand-new --session-id the same way
	// spawnShared does for a first spawn. Non-claude harnesses leave
	// storedSessionID empty so the tmux-TUI driver's post-hoc discovery
	// (IDs.Get()=="") re-arms, matching a genuinely fresh spawn.
	isClaude := rec.Harness == "" || rec.Harness == "claude"
	args := ResumeArgs(rec.ClaudeArgs, "")
	storedSessionID := ""
	if isClaude {
		sessionID := session.NewID()
		args = append(args, "--session-id", sessionID)
		storedSessionID = sessionID
	}

	if err := m.sup.SpawnAgent(SpawnRequest{
		Name:       rec.Name,
		ClaudeArgs: args,
		WorkDir:    rec.Workspace,
		Env:        rec.Env,
		WebPort:    rec.WebPort,
		WebToken:   m.webToken,
		Harness:    rec.Harness,
	}); err != nil {
		return fmt.Errorf("respawning %q after reset: %w (re-run 'leo agent reset %s' to retry)", name, err, name)
	}

	rec.ClaudeArgs = args
	rec.SessionID = storedSessionID
	rec.NoResume = false
	if err := agentstore.Save(cfg.HomePath, rec); err != nil {
		log.Printf("agent %q reset but agentstore.Save failed: %v — flag may persist until next save", rec.Name, err)
	}
	return nil
}

// Restart bounces a currently-running agent's tmux session while preserving
// its conversation: it stops the live process, then respawns with --resume
// (the same jsonl-preferring resume logic as Resume), leaving Suspended and
// NoResume untouched — unlike Suspend/Reset, Restart never marks the record
// suspended and never starts a fresh session. Errors when name has no live
// process (suspended/stopped/unknown agents are not restartable — callers
// driving --all should treat that as a skip, not a failure). If the respawn
// fails after the stop succeeds, the record is left exactly as before (still
// pointing at the same SessionID/ClaudeArgs) so the agent is not left flagged
// as suspended; re-running Restart retries the respawn since the agent is no
// longer live.
//
// Restart is also the one deliberate "apply current config" verb: for an
// agent spawned from a template that still exists in the current config,
// with its effective harness unchanged, it rebuilds ClaudeArgs/Env from
// today's defaults+template cascade (see resolveRestartArgs) before
// resuming — so a harness_options/model change made after the agent started
// takes effect on restart. Ad-hoc agents (no Template), agents whose
// template was deleted, and agents whose effective harness changed all fall
// back to the stored args/env unchanged, since re-resolving those safely
// isn't possible.
func (m *Manager) Restart(name string) error {
	if _, ok := m.sup.EphemeralAgents()[name]; !ok {
		return fmt.Errorf("agent %q is not running", name)
	}
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
		return fmt.Errorf("no agentstore record for %q (cannot restart an unpersisted agent)", name)
	}

	if err := m.sup.StopAgent(name); err != nil {
		return fmt.Errorf("stopping agent for restart: %w", err)
	}

	// Claude-specific resume mechanic, matching Resume: prefer the newest
	// on-disk jsonl transcript over the stored SessionID so a /clear session
	// the store never saw is still picked up.
	resumeID := rec.SessionID
	isClaude := rec.Harness == "" || rec.Harness == "claude"
	if isClaude {
		if latestID, _, err := session.LatestSession(rec.Workspace, 0); err == nil && latestID != "" {
			resumeID = latestID
		}
	}

	args, env := resolveRestartArgs(cfg, rec, m.webToken)
	if isClaude {
		args = ResumeArgs(args, resumeID)
	}

	if err := m.sup.SpawnAgent(SpawnRequest{
		Name:       rec.Name,
		ClaudeArgs: args,
		WorkDir:    rec.Workspace,
		Env:        env,
		WebPort:    rec.WebPort,
		WebToken:   m.webToken,
		Harness:    rec.Harness,
	}); err != nil {
		return fmt.Errorf("respawning %q after restart: %w (re-run 'leo agent restart %s' to retry)", name, err, name)
	}

	rec.ClaudeArgs = args
	rec.Env = env
	rec.SessionID = resumeID
	if err := agentstore.Save(cfg.HomePath, rec); err != nil {
		log.Printf("agent %q restarted but agentstore.Save failed: %v — stored SessionID may lag until next save", name, err)
	}
	return nil
}

// resolveRestartArgs decides whether Restart can safely re-resolve rec's
// ClaudeArgs/Env from cfg's current template+defaults cascade, or must fall
// back to what's already stored. It returns args WITHOUT any --resume/
// --session-id mutation — Restart applies that afterward, same as a fresh
// resolveRestartArgs-less restart did.
//
// Re-resolution requires all of:
//   - rec.Template is set (ad-hoc/from-agent spawns have no template to
//     re-resolve against)
//   - that template still exists in cfg (a deleted template can't be
//     re-resolved; keep the agent on its last-known-good args)
//   - the effective harness is unchanged (swapping harness under a resumed
//     conversation is not supported — the resume mechanic, MCP bridge, and
//     env shape all differ per harness)
//
// When it re-resolves, env is rebuilt layer by layer exactly like a fresh
// spawn (see spawnShared/spawnWorktreeCore): the harness env and template env
// as the base, then rec.InheritedEnv re-pruned against the CURRENT harness
// env (not the stale spawn-time pruning — a harness env key that didn't
// exist yet at spawn time must still be able to win here), then rec.SpawnEnv
// (the caller's explicit --env overrides) always winning on top. Legacy
// records (both SpawnEnv and InheritedEnv nil, Env non-nil — written before
// either field existed) still get re-resolved args, but keep their stored Env
// unchanged: leo has no record of which layer produced which key, so
// reconstructing it here could silently drop caller-supplied env instead of
// just leaving it as-is.
func resolveRestartArgs(cfg *config.Config, rec agentstore.Record, webToken string) (args []string, env map[string]string) {
	fallback := func() ([]string, map[string]string) { return rec.ClaudeArgs, rec.Env }

	if rec.Template == "" {
		return fallback()
	}
	tmpl, ok := cfg.Templates[rec.Template]
	if !ok {
		return fallback()
	}
	normalizeHarness := func(h string) string {
		if h == "" {
			return "claude"
		}
		return h
	}
	if normalizeHarness(cfg.TemplateHarness(tmpl)) != normalizeHarness(rec.Harness) {
		return fallback()
	}

	// Empty prompt: restart resumes an existing conversation, it never
	// re-sends an opening prompt. No --session-id is appended here (that's
	// first-spawn-only) — Restart's caller applies --resume itself.
	newArgs, newHarnessEnv := BuildTemplateArgs(cfg, tmpl, rec.Name, rec.Workspace, "", webToken)
	if newArgs == nil {
		// BuildTemplateArgs already logged the failure; keep the agent alive
		// on its last-known-good args rather than respawning it broken.
		return fallback()
	}

	newEnv := rec.Env
	if rec.SpawnEnv != nil || rec.InheritedEnv != nil || rec.Env == nil {
		inherited := pruneEnv(rec.InheritedEnv, newHarnessEnv)
		newEnv = mergeEnv(mergeEnv(mergeEnv(newHarnessEnv, tmpl.Env), inherited), rec.SpawnEnv)
	}

	return newArgs, newEnv
}

// RestartResult summarizes the outcome of a RestartAll batch: which agents
// were actually bounced, which were skipped because they weren't running
// (suspended/stopped agents are not restartable), and any per-agent errors
// encountered along the way. A batch failure on one agent never aborts the
// rest — every running agent gets its own attempt.
type RestartResult struct {
	Restarted []string
	Skipped   []string
	Failed    map[string]error
}

// RestartAll bounces every live agent (see Restart), skipping only the
// intentionally-down ones: records whose Status is "suspended" or "stopped".
// Everything else the supervisor still holds live — including "starting" and
// "restarting" (crash-loop backoff) agents — is bounced; restarting a
// backing-off agent simply short-circuits its crash loop with a fresh spawn.
// Failures are isolated per-agent so one bad respawn doesn't block the rest of
// the batch.
func (m *Manager) RestartAll() RestartResult {
	result := RestartResult{Failed: map[string]error{}}
	for _, rec := range m.List() {
		if rec.Status == "suspended" || rec.Status == "stopped" {
			result.Skipped = append(result.Skipped, rec.Name)
			continue
		}
		if err := m.Restart(rec.Name); err != nil {
			result.Failed[rec.Name] = err
			continue
		}
		result.Restarted = append(result.Restarted, rec.Name)
	}
	return result
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

// SessionName returns the tmux session name for an agent. Idempotent: if
// `name` is already a fully-qualified agent name (i.e. begins with "leo-"),
// it is returned unchanged so callers can pass either an agent.Record.Name
// or a bare identifier without producing "leo-leo-…" double prefixes.
func SessionName(name string) string {
	if strings.HasPrefix(name, "leo-") {
		return name
	}
	return "leo-" + name
}

// SessionName returns the tmux session name for an agent.
func (m *Manager) SessionName(name string) string {
	return SessionName(name)
}

// handleForRecord builds the harness.SessionHandle a SessionDriver needs to
// act on an agentstore record — shared by ResolveHandle (web message
// dispatch) and Logs (non-tmux history tail).
func (m *Manager) handleForRecord(homePath string, rec agentstore.Record) harness.SessionHandle {
	return harness.SessionHandle{
		Kind:        harness.KindAgent,
		Name:        rec.Name,
		TmuxSession: m.SessionName(rec.Name),
		Workspace:   rec.Workspace,
		HomePath:    homePath,
		Env:         rec.Env,
		IDs:         NewAgentIDs(homePath, rec.Name),
	}
}

// ResolveHandle resolves an agent name to its harness name and the
// harness.SessionHandle a SessionDriver needs to deliver a message to it.
// Implements the web package's agent-side handle resolver seam (mirrors the
// process-side resolver wired at service boot): ok=false means "not an
// ephemeral agent" (unknown name, or no agentstore record yet), in which
// case the caller falls back to today's tmux behavior.
func (m *Manager) ResolveHandle(name string) (string, harness.SessionHandle, bool) {
	cfg, err := m.cfgLoader()
	if err != nil {
		return "", harness.SessionHandle{}, false
	}
	records, err := agentstore.Load(agentstore.FilePath(cfg.HomePath))
	if err != nil {
		return "", harness.SessionHandle{}, false
	}
	rec, ok := records[name]
	if !ok {
		return "", harness.SessionHandle{}, false
	}
	harnessName := rec.Harness
	if harnessName == "" {
		harnessName = "claude"
	}
	return harnessName, m.handleForRecord(cfg.HomePath, rec), true
}

// Logs returns the last `lines` lines of output from the agent's tmux pane.
// Every harness (claude, codex, opencode) drives its TUI inside a resident
// tmux pane, so capture-pane is the single source of truth here. If lines <=
// 0, returns the whole scrollback.
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
	// Best-effort: fall back to the active-pane target if the concrete pane
	// can't be resolved, rather than erroring louder than before ResolvePane
	// existed.
	target := tmux.ResolvePaneOrFallback(context.Background(), tmuxPath, session)
	subArgs := []string{"capture-pane", "-t", target, "-p"}
	if lines > 0 {
		subArgs = append(subArgs, "-S", fmt.Sprintf("-%d", lines))
	} else {
		subArgs = append(subArgs, "-S", "-")
	}

	out, err := exec.Command(tmuxPath, tmux.Args(subArgs...)...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("tmux capture-pane: %s", string(out))
	}
	return string(out), nil
}

// Rename changes an agent's identity. The agent is fuzzy-resolved from query,
// the new name is normalized and checked for collisions, the live supervisor
// state is renamed in place (zero restart) when the agent is running, and the
// persisted record is re-keyed with its --name flag rewritten. Stopped worktree
// agents skip the supervisor and only re-key the store.
func (m *Manager) Rename(query, rawNewName string) (Record, error) {
	rec, err := m.Resolve(query)
	if err != nil {
		// Resolve only matches live agents. A stopped worktree agent is kept
		// in the store with Stopped=true, so fall back to an exact agentstore
		// lookup (raw and normalized) before surfacing the resolve error.
		fallback, ok := m.resolveStored(query)
		if !ok {
			return Record{}, err
		}
		rec = fallback
	}
	oldName := rec.Name

	newName, err := NormalizeAgentName(rawNewName)
	if err != nil {
		return Record{}, err
	}
	if newName == oldName {
		return Record{}, fmt.Errorf("%w: %q", ErrAgentNameUnchanged, oldName)
	}

	cfg, err := m.cfgLoader()
	if err != nil {
		return Record{}, fmt.Errorf("loading config: %w", err)
	}

	// Pre-check the store for a newName collision BEFORE touching the live
	// supervisor. RenameAgent only checks live state/reservations, so without
	// this a live agent could be renamed into a STOPPED record's name —
	// tmux/maps would re-key but agentstore.Rename would then error on the
	// collision, leaving supervisor and store inconsistent. A missing store
	// file (no agents persisted yet) yields a non-nil empty map alongside an
	// ErrNotExist, so it correctly reports "no collision"; any other load
	// error is surfaced.
	records, err := agentstore.Load(agentstore.FilePath(cfg.HomePath))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Record{}, fmt.Errorf("loading agent records: %w", err)
	}
	if _, exists := records[newName]; exists {
		return Record{}, fmt.Errorf("%w: %q", ErrAgentNameTaken, newName)
	}

	if _, live := m.sup.EphemeralAgents()[oldName]; live {
		if err := m.sup.RenameAgent(oldName, newName); err != nil {
			return Record{}, fmt.Errorf("renaming running agent: %w", err)
		}
	} else {
		// Not live: sup.RenameAgent is never called, so the announce it would
		// otherwise make (agent_stopped(oldName) then agent_spawned(newName))
		// happens here instead — see the finding this closes: a stopped
		// agent's rename used to re-key the store in complete silence.
		m.announceRename(cfg, rec, oldName, newName)
	}

	if err := agentstore.Rename(cfg.HomePath, oldName, newName, func(r agentstore.Record) agentstore.Record {
		r.Name = newName
		r.ClaudeArgs = rewriteNameArg(r.ClaudeArgs, newName)
		return r
	}); err != nil {
		return Record{}, fmt.Errorf("persisting rename: %w", err)
	}

	rec.Name = newName
	return rec, nil
}

// announceRename publishes the same agent_stopped-then-agent_spawned
// sequence service.Supervisor.RenameAgent publishes for a live rename,
// for the not-live path where that call never happens. Unlike the live
// path (which zeroes Template/Repo/Branch/Model because the agentstore
// record isn't re-keyed yet when it publishes), rec here already carries
// them from the stored record, so the spawned view is fully populated.
func (m *Manager) announceRename(cfg *config.Config, rec Record, oldName, newName string) {
	m.publish(observe.Event{
		Type:    observe.EventAgentStopped,
		Payload: &observe.AgentStoppedPayload{Agent: oldName},
	})

	agent := observe.Agent{
		Name:      newName,
		Template:  rec.Template,
		Repo:      rec.Repo,
		Workspace: rec.Workspace,
		Branch:    rec.Branch,
		Status:    observe.StatusStopped,
	}
	if cfg != nil && rec.Template != "" {
		if tmpl, ok := cfg.Templates[rec.Template]; ok {
			agent.Model = cfg.TemplateModel(tmpl)
			agent.Harness = cfg.TemplateHarness(tmpl)
		}
	}
	m.publish(observe.Event{
		Type:    observe.EventAgentSpawned,
		Payload: &observe.AgentSpawnedPayload{Agent: agent},
	})
}

// resolveStored finds a persisted (possibly stopped) agent by exact name when
// the live resolver cannot. It tries the raw query first, then the normalized
// form, so both "leo-foo" and "foo" locate the same record. Returns false when
// the store is unreadable or no exact match exists.
func (m *Manager) resolveStored(query string) (Record, bool) {
	cfg, err := m.cfgLoader()
	if err != nil {
		return Record{}, false
	}
	stored, err := agentstore.Load(agentstore.FilePath(cfg.HomePath))
	// A missing store file means nothing is persisted yet — treat as no match,
	// not a hard failure. A real load error (parse, permission) is also treated
	// as "not found" here so Rename surfaces the original resolve error rather
	// than an opaque store error; loadLocked returns a non-nil empty map on
	// error so the lookup below simply finds nothing.
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return Record{}, false
	}

	candidates := []string{strings.TrimSpace(query)}
	if norm, err := NormalizeAgentName(query); err == nil {
		candidates = append(candidates, norm)
	}
	for _, name := range candidates {
		if _, ok := stored[name]; ok {
			r := Record{Name: name}
			mergeStored(&r, stored)
			return r, true
		}
	}
	return Record{}, false
}

// rewriteNameArg returns a copy of args with the value following --name replaced
// by newName. If --name is absent the args are returned unchanged.
func rewriteNameArg(args []string, newName string) []string {
	out := make([]string, len(args))
	copy(out, args)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == "--name" {
			out[i+1] = newName
			break
		}
	}
	return out
}
