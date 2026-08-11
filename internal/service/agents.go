package service

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/git"
	"github.com/blackpaw-studio/leo/internal/session"
)

// agentSpawner is the minimal supervisor surface RestoreAgents needs.
// Extracted as an interface so tests can inject a fake and avoid spinning up
// real tmux-backed supervisor goroutines.
type agentSpawner interface {
	SpawnAgent(spec daemon.AgentSpawnSpec) error
}

// RestoreAgents respawns ephemeral agents from a previous daemon run, using
// each record's SessionID to pass `--resume <sid>` so claude rehydrates the
// prior conversation.
//
// Skip rules:
//   - Worktree record with a missing workspace directory: drop it, nothing
//     to reattach to.
//   - Record marked Stopped=true: the user stopped it explicitly; keep the
//     record (worktree agents need it for `leo agent prune`) but do not
//     resurrect the agent.
//
// For every other record the function rewrites the stored claude args to
// strip any prior `--session-id` / `--resume` flag and append `--resume
// <SessionID>`, then calls SpawnAgent. Records whose SessionID is empty
// (legacy records from pre-resume daemon versions) respawn without a resume
// flag so the agent still comes back, just with a fresh conversation.
//
// After all records are processed, `git worktree prune` runs once per unique
// canonical path so git's administrative state matches the filesystem.
func RestoreAgents(homePath, tmuxPath, webToken string, sv agentSpawner) int {
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
				// Worktree directory gone — nothing to reattach to.
				// Drop the record; git's own metadata is cleaned up by
				// the `git worktree prune` pass below.
				fmt.Fprintf(os.Stderr, "restore: dropping worktree record %q (workspace missing: %s)\n", name, rec.Workspace)
				agentstore.Remove(homePath, name)
				continue
			}
		}

		if rec.Stopped {
			// User stopped this agent explicitly. Skip respawn.
			continue
		}

		if rec.Suspended {
			// Daemon idle-suspended this agent. Keep the record; auto-wake on
			// the next incoming message resumes it. Do not resurrect at boot.
			continue
		}

		// A tmux session that outlived the previous daemon (launchctl
		// kickstart -k / a crash SIGKILLs the daemon but leaves the
		// independent tmux server and its sessions running) is healthy and
		// detached from the old process. Re-adopt it instead of killing and
		// respawning, so `leo update` / `leo service restart` no longer
		// disrupt every running agent. When no live session exists there is
		// nothing to kill — SpawnAgent creates a fresh one that resumes the
		// prior conversation via --resume.
		adopt := tmuxPath != "" && tmuxHasSession(tmuxPath, agent.SessionName(name))

		// NoResume short-circuits the resume lookup entirely. It is set by
		// the supervisor when the previous spawn quick-exited while resuming
		// — the jsonl on disk is poisoned (e.g. claude TUI bug rehydrating
		// scheduled-tasks state), so passing --resume here would just
		// reproduce the crash. Spawn fresh and clear the flag so the next
		// healthy session can be resumed normally.
		var resumeID string
		switch {
		case rec.NoResume:
			fmt.Fprintf(os.Stderr, "restore: agent %q skipping --resume (NoResume flag set after prior quick-exit) — respawning fresh\n", name)
			updated := rec
			updated.NoResume = false
			updated.SessionID = ""
			if err := agentstore.Save(homePath, updated); err != nil {
				fmt.Fprintf(os.Stderr, "restore: agent %q could not clear NoResume flag: %v\n", name, err)
			}
		case rec.SessionPinned:
			// A template switch pinned this record to the session it restored
			// for the arriving template. Take it verbatim: the jsonl scan
			// below is workspace-wide and template-blind, so right after a
			// switch it would hand back the transcript of the template just
			// left and quietly undo the swap. One-shot, like NoResume.
			resumeID = rec.SessionID
			updated := rec
			updated.SessionPinned = false
			if err := agentstore.Save(homePath, updated); err != nil {
				fmt.Fprintf(os.Stderr, "restore: agent %q could not clear SessionPinned flag: %v\n", name, err)
			}
		default:
			// Prefer the newest jsonl in claude's project directory for this
			// workspace over the stored SessionID — catches sessions created
			// via /clear that agentstore never saw. maxAge=0 disables the
			// staleness drop; agents are short-lived and the newest jsonl is
			// virtually always the one we want. This jsonl scan is a
			// claude-specific resume mechanic; non-claude records keep their
			// stored SessionID untouched here — the supervisor's
			// SessionArgsRefresher is responsible for injecting resume tokens
			// into their args at spawn time, so restore must not pre-inject
			// them itself.
			resumeID = rec.SessionID
			isClaude := rec.Harness == "" || rec.Harness == "claude"
			if isClaude {
				if latestID, _, err := session.LatestSession(rec.Workspace, 0); err == nil && latestID != "" {
					if latestID != rec.SessionID {
						updated := rec
						updated.SessionID = latestID
						if err := agentstore.Save(homePath, updated); err != nil {
							fmt.Fprintf(os.Stderr, "restore: agent %q could not persist latest session id: %v\n", name, err)
						}
					}
					resumeID = latestID
				}
			}
		}

		args := rec.ClaudeArgs
		if rec.Harness == "" || rec.Harness == "claude" {
			args = agent.ResumeArgs(rec.ClaudeArgs, resumeID)
		}
		if resumeID == "" && !rec.NoResume && !rec.SessionPinned {
			fmt.Fprintf(os.Stderr, "restore: agent %q has no session_id (legacy record) — respawning with a fresh claude session\n", name)
		}

		spec := daemon.AgentSpawnSpec{
			Name:       rec.Name,
			ClaudeArgs: args,
			WorkDir:    rec.Workspace,
			Env:        rec.Env,
			WebPort:    rec.WebPort,
			WebToken:   webToken,
			Adopt:      adopt,
			Harness:    rec.Harness,
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
