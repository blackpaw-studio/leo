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

	// ErrNotWorktreeAgent is returned by Prune when the agent was spawned
	// without --worktree. There is nothing to prune — the canonical clone is
	// shared and must not be deleted.
	ErrNotWorktreeAgent = errors.New("agent has no worktree to prune")

	ErrBranchCheckedOut = git.ErrBranchCheckedOut
	ErrWorktreeDirty    = git.ErrWorktreeDirty
	ErrBranchNotMerged  = git.ErrBranchNotMerged
	ErrBranchNotFound   = git.ErrBranchNotFound
)
