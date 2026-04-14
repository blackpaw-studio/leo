package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/git"
)

// RestoreAgents restores ephemeral agents from a previous daemon run.
//
// Shared-workspace agents (rec.Branch == ""): dead tmux sessions are cleaned
// up from agents.json, live sessions are re-registered with the supervisor.
// This matches pre-worktree behavior.
//
// Worktree agents (rec.Branch != ""): the record is preserved across restarts
// even when the session is dead, because the agent's on-disk worktree still
// exists and the user may want to prune it. We only drop worktree records
// whose Workspace path is missing on disk — those are unrecoverable.
//
// After all records are processed, `git worktree prune` runs once per unique
// canonical path so git's administrative state matches the filesystem.
func RestoreAgents(homePath, tmuxPath string, sv *Supervisor) int {
	path := agentstore.FilePath(homePath)
	records, err := agentstore.Load(path)
	if err != nil || len(records) == 0 {
		return 0
	}

	restored := 0
	canonicals := make(map[string]struct{})

	for name, rec := range records {
		isWorktree := rec.Branch != ""
		if isWorktree {
			canonicals[rec.CanonicalPath] = struct{}{}
			if _, err := os.Stat(rec.Workspace); err != nil {
				// Worktree directory gone — nothing to reattach to. Drop
				// the record; git's own metadata is cleaned up by the
				// `git worktree prune` pass below.
				fmt.Fprintf(os.Stderr, "restore: dropping worktree record %q (workspace missing: %s)\n", name, rec.Workspace)
				agentstore.Remove(homePath, name)
				continue
			}
		}

		sessionName := fmt.Sprintf("leo-%s", name)
		sessionAlive := false
		if tmuxPath != "" {
			check := exec.Command(tmuxPath, "has-session", "-t", sessionName)
			sessionAlive = check.Run() == nil
		}

		if !sessionAlive {
			if isWorktree {
				// Keep the record so `leo agent prune` can find it. No
				// supervisor registration — the session is gone.
				continue
			}
			agentstore.Remove(homePath, name)
			continue
		}

		spec := daemon.AgentSpawnSpec{
			Name:       rec.Name,
			ClaudeArgs: rec.ClaudeArgs,
			WorkDir:    rec.Workspace,
			Env:        rec.Env,
			WebPort:    rec.WebPort,
		}
		if err := sv.SpawnAgent(spec); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to restore agent %q: %v\n", name, err)
			if !isWorktree {
				agentstore.Remove(homePath, name)
			}
			continue
		}
		restored++
	}

	pruneCanonicalWorktrees(canonicals)
	return restored
}

// pruneCanonicalWorktrees runs `git worktree prune` against each unique
// canonical path seen during restore. A 10s per-repo timeout keeps a hung
// filesystem from blocking daemon startup indefinitely.
func pruneCanonicalWorktrees(paths map[string]struct{}) {
	for canonical := range paths {
		if canonical == "" {
			continue
		}
		if _, err := os.Stat(canonical); err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := git.PruneWorktrees(ctx, canonical); err != nil {
			fmt.Fprintf(os.Stderr, "restore: git worktree prune %s failed: %v\n", canonical, err)
		}
		cancel()
	}
}
