package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/git"
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
//   - Record marked Stopped=true with an empty StoppedReason: dormant —
//     either the user stopped it explicitly, or the idle sweep did (either
//     way, WakeOnMessage decides whether a later message wakes it, not
//     RestoreAgents). Keep the record (needed for `leo agent start`/`delete`)
//     but do not resurrect the agent at boot.
//   - Record marked Stopped=true with a non-empty StoppedReason: the SYSTEM
//     stopped it after a failed boot-time restore (missing workspace, spawn
//     failure — see markFailedRestore). This is retried on every subsequent
//     boot rather than permanently skipped, so a transient failure (a late
//     NAS mount, a tmux hiccup) self-heals once the underlying condition
//     clears instead of requiring an operator to run `leo agent restart` by
//     hand for every affected agent. A repeat failure simply re-marks the
//     record via markFailedRestore, same as the first time.
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
		}

		if isWorktree && workspaceMissing(name, rec.Workspace) {
			// Worktree directory gone — nothing to reattach to.
			// Drop the record; git's own metadata is cleaned up by
			// the `git worktree prune` pass below.
			fmt.Fprintf(os.Stderr, "restore: dropping worktree record %q (workspace missing: %s)\n", name, rec.Workspace)
			agentstore.Remove(homePath, name)
			continue
		}

		if rec.Stopped {
			if !rec.IsFailedRestore() {
				// Dormant (user- or idle-sweep-stopped). Skip respawn — this
				// guard MUST run before the missing-workspace check below, or
				// a dormant shared-workspace agent whose workspace is
				// transiently missing at boot (e.g. a late NAS mount) would
				// get re-marked via markFailedRestore instead of staying
				// exactly as dormant as it already was.
				continue
			}
			// System-marked failed restore (see markFailedRestore): fall
			// through and retry the spawn below. A repeat failure simply
			// re-marks the record, same as the first failure.
		}

		if !isWorktree && workspaceMissing(name, rec.Workspace) {
			// Shared-workspace record whose workspace directory is gone
			// (e.g. an unmounted NAS at boot time). Keep the record — it is
			// the agent's only surviving identity (template, repo, session
			// id, env) — but mark it stopped-by-the-system so it is visible
			// and recoverable (`leo agent restart`) instead of resurrecting
			// a doomed tmux session against a missing directory.
			fmt.Fprintf(os.Stderr, "restore: agent %q workspace missing (%s) — leaving stopped, not spawning\n", name, rec.Workspace)
			markFailedRestore(homePath, rec, fmt.Sprintf("workspace missing: %s", rec.Workspace))
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
			// Drop the switch pin too: it guards a session this path is
			// discarding, and a pin left behind would suppress the transcript
			// scan on the NEXT restore, starting a brand-new conversation
			// instead of picking up the one the agent actually ran.
			updated.SessionPinnedAt = nil
			if err := agentstore.Save(homePath, updated); err != nil {
				fmt.Fprintf(os.Stderr, "restore: agent %q could not clear NoResume flag: %v\n", name, err)
			}
		default:
			// Prefer the newest jsonl in claude's project directory for this
			// workspace over the stored SessionID — catches sessions created
			// via /clear that agentstore never saw — while honoring a template
			// switch's pin for transcripts that predate it. This jsonl scan is
			// a claude-specific resume mechanic; non-claude records keep their
			// stored SessionID untouched, since the supervisor's
			// SessionArgsRefresher injects their resume tokens at spawn time
			// and restore must not pre-inject them itself. See agent.ResumeIDFor.
			resumeID = agent.ResumeIDFor(rec)
			if resumeID != rec.SessionID || rec.SessionPinnedAt != nil {
				updated := rec
				updated.SessionID = resumeID
				updated.SessionPinnedAt = nil
				if err := agentstore.Save(homePath, updated); err != nil {
					fmt.Fprintf(os.Stderr, "restore: agent %q could not persist resolved session id: %v\n", name, err)
				}
			}
		}

		args := rec.ClaudeArgs
		if rec.Harness == "" || rec.Harness == "claude" {
			args = agent.ResumeArgs(rec.ClaudeArgs, resumeID)
		}
		if resumeID == "" && !rec.NoResume && rec.SessionPinnedAt == nil {
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
				// Keep the record instead of deleting it: a transient
				// boot-time spawn failure (tmux server hiccup, etc.) must
				// not permanently destroy the agent's identity. Mark it
				// stopped-by-the-system so it is visible and recoverable via
				// `leo agent restart`.
				markFailedRestore(homePath, rec, fmt.Sprintf("restore spawn failed: %v", err))
			}
			continue
		}
		if rec.Stopped {
			// A retried failed-restore record that just came back healthy —
			// clear the system-stopped markers so it stops being flagged as
			// stuck/recoverable now that it is genuinely running again.
			clearFailedRestore(homePath, rec)
		}
		restored++
	}

	pruneCanonicalWorktrees(canonicals)
	return restored
}

// workspaceMissing reports whether rec's workspace directory is confirmed
// gone (fs.ErrNotExist). Any other stat error — EACCES, EIO, a hung or
// timed-out NFS mount — is NOT treated as missing: doing so would condemn a
// healthy-but-transiently-unreachable workspace to Stopped state on every
// boot. Such errors are logged and treated as "present" so the caller falls
// through to its normal spawn attempt, same as before this check existed.
func workspaceMissing(name, workspace string) bool {
	_, err := os.Stat(workspace)
	if err == nil {
		return false
	}
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	fmt.Fprintf(os.Stderr, "restore: agent %q workspace stat failed (%v) — treating as present\n", name, err)
	return false
}

// markFailedRestore persists rec with Stopped=true and StoppedReason set to
// reason, so a shared-workspace agent that failed to come back at boot stays
// visible (Manager.List) and recoverable (`leo agent restart`) instead of
// being deleted outright. Save failures are logged, not fatal — the caller
// has already decided not to spawn this record either way.
func markFailedRestore(homePath string, rec agentstore.Record, reason string) {
	if rec.Stopped && rec.StoppedReason == reason {
		// Already marked with this exact reason (e.g. a persistently broken
		// workspace re-failing on every boot) — the record on disk already
		// matches; skip the redundant Save. Caller has already logged the
		// condition to stderr, so the operator still sees it every boot.
		return
	}
	updated := rec
	updated.Stopped = true
	updated.StoppedReason = reason
	if err := agentstore.Save(homePath, updated); err != nil {
		fmt.Fprintf(os.Stderr, "restore: agent %q could not persist failed-restore state: %v\n", rec.Name, err)
	}
}

// clearFailedRestore clears Stopped/StoppedReason on a record whose retried
// boot-time spawn just succeeded, mirroring the clear Manager.Restart does
// for the same recovery path. Save failures are logged, not fatal — the
// spawn already succeeded either way.
func clearFailedRestore(homePath string, rec agentstore.Record) {
	updated := rec
	updated.Stopped = false
	updated.StoppedReason = ""
	if err := agentstore.Save(homePath, updated); err != nil {
		fmt.Fprintf(os.Stderr, "restore: agent %q could not clear failed-restore state: %v\n", rec.Name, err)
	}
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
