package agent

import (
	"errors"

	"github.com/blackpaw-studio/leo/internal/git"
)

// Sentinel errors for worktree-aware spawn and prune flows. These are re-exported
// from internal/git where the underlying git invocation produces them, so callers
// (daemon, CLI, web) can match via errors.Is without importing internal/git directly.
var (
	// ErrWorktreeRequiresSlash is returned by Spawn when --worktree is combined
	// with a bare-name repo. Worktrees only make sense for owner/repo clones.
	ErrWorktreeRequiresSlash = errors.New("--worktree requires an owner/repo spec")

	// ErrAgentStillRunning is returned by Prune when the target agent has a live
	// tmux session. Operators must call Stop first (or pass the stop+prune
	// combo from the CLI).
	ErrAgentStillRunning = errors.New("agent is still running; stop it first")

	// ErrNotWorktreeAgent is unused by Manager.Delete (which now accepts
	// shared-workspace agents too) but kept for callers/wire compatibility
	// that still match on it.
	ErrNotWorktreeAgent = errors.New("agent has no worktree to prune")

	// ErrAgentNameTaken is returned by Manager.Rename when the target name
	// already exists (live or persisted). Callers may map it to HTTP 409.
	ErrAgentNameTaken = errors.New("agent name already exists")

	// ErrAgentNameUnchanged is returned by Manager.Rename when the new name
	// equals the current name. Callers may map it to HTTP 400.
	ErrAgentNameUnchanged = errors.New("agent name unchanged")

	ErrBranchCheckedOut = git.ErrBranchCheckedOut
	ErrWorktreeDirty    = git.ErrWorktreeDirty
	ErrBranchNotMerged  = git.ErrBranchNotMerged
	ErrBranchNotFound   = git.ErrBranchNotFound

	// ErrSourceAgentNotFound is returned by from-agent worktree spawns when
	// the named source agent has no agentstore record.
	ErrSourceAgentNotFound = errors.New("source agent not found")

	// ErrSourceNotGitRepo is returned by from-agent worktree spawns when the
	// source agent's workspace is not a git repository — there is nothing to
	// add a worktree to.
	ErrSourceNotGitRepo = errors.New("source agent's workspace is not a git repository")

	// ErrAgentStopped is returned by Manager.Restart and Manager.Logs when the
	// resolved agent has no live supervisor state because it is dormant
	// (stopped). These operations require a live process, so they reject
	// with a typed error the caller can map to a 4xx telling the operator to
	// start the agent first, rather than a bare 500.
	ErrAgentStopped = errors.New("agent is stopped")

	// ErrAgentNotStopped is returned by Manager.Start when the resolved agent
	// record exists but is not dormant (it is already running). Callers map
	// it to a 4xx telling the operator there is nothing to start, rather
	// than a bare 500.
	ErrAgentNotStopped = errors.New("agent is not stopped")

	// ErrAgentAlreadyRunning is returned by Manager.Start when the target
	// agent is currently live. Callers map it to a 4xx (409) telling the
	// operator the agent is already up rather than a bare 500.
	ErrAgentAlreadyRunning = errors.New("agent is already running")

	// ErrAgentNotRunning is returned when a persisted record exists for the
	// name but has no live supervisor state (e.g. a stopped agent) and the
	// requested operation requires a live process.
	ErrAgentNotRunning = errors.New("agent is not running")
)
