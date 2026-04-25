package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/git"
	"github.com/blackpaw-studio/leo/internal/session"
	"github.com/blackpaw-studio/leo/internal/tmux"
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

		// If a tmux session somehow survived (daemon crashed rather than
		// shut down cleanly), kill it so SpawnAgent starts a fresh one
		// that resumes the claude session cleanly.
		if tmuxPath != "" {
			sessionName := agent.SessionName(name)
			_ = exec.Command(tmuxPath, tmux.Args("kill-session", "-t", sessionName)...).Run()
		}

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
		default:
			// Prefer the newest jsonl in claude's project directory for this
			// workspace over the stored SessionID — catches sessions created
			// via /clear that agentstore never saw. maxAge=0 disables the
			// staleness drop; agents are short-lived and the newest jsonl is
			// virtually always the one we want.
			resumeID = rec.SessionID
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

		args := argsWithResume(rec.ClaudeArgs, resumeID)
		if resumeID == "" && !rec.NoResume {
			fmt.Fprintf(os.Stderr, "restore: agent %q has no session_id (legacy record) — respawning with a fresh claude session\n", name)
		}

		spec := daemon.AgentSpawnSpec{
			Name:       rec.Name,
			ClaudeArgs: args,
			WorkDir:    rec.Workspace,
			Env:        rec.Env,
			WebPort:    rec.WebPort,
			WebToken:   webToken,
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

// argsWithResume rewrites stored claude args so the restored agent resumes the
// prior session. Any existing `--session-id` or `--resume` pair is stripped
// (defensive: we don't want to accidentally pass two session-selection flags)
// before appending `--resume <sessionID>`. An empty sessionID returns the args
// with session flags stripped — the caller has already decided to do a fresh
// spawn.
func argsWithResume(args []string, sessionID string) []string {
	cleaned := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		if (args[i] == "--session-id" || args[i] == "--resume") && i+1 < len(args) {
			i++ // skip the value too
			continue
		}
		cleaned = append(cleaned, args[i])
	}
	if sessionID == "" {
		return cleaned
	}
	return append(cleaned, "--resume", sessionID)
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
