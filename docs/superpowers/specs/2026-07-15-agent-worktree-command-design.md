# `leo agent worktree` — spawn a worktree agent from an existing agent

**Date:** 2026-07-15
**Status:** Approved

## Problem

Spawning a worktree agent today requires re-specifying the template and the
full owner/repo even when an existing agent already knows both:

```
leo agent spawn claude evandcoleman/chronicle --worktree a11y
```

The operator's mental model is "branch off the chronicle agent". The command
should match:

```
leo agent worktree chronicle a11y
```

It must also work for agents whose workspace is a git repo but whose record
lacks an owner/repo (plain-name spawns like `plex`, or template agents) — the
current `--worktree` path hard-requires `owner/repo`
(`ErrWorktreeRequiresSlash`).

## Command

```
leo agent worktree <agent> <branch> [--base <ref>] [--name <n>] [--prompt <p>] [--env k=v] [--host <h>] [--json]
```

- New subcommand under `leo agent`, alongside `spawn`.
- Follows spawn's host-dispatch pattern: for a remote host, forward
  `agent worktree <agent> <branch> [flags]` over SSH verbatim.

## Resolution

Look up `<agent>` in the agentstore (same resolution as other agent
subcommands):

1. **Not found** → error suggesting `leo agent list`. Lookup is by name
   (verbatim, then normalized) against the agentstore — deliberately narrower
   than live-agent resolution so stopped agents remain valid sources.
2. **Source is itself a worktree agent** (record has `CanonicalPath`) →
   canonical = its `CanonicalPath`. This allows branching off e.g.
   `chronicle-a11y`; pass `--base a11y` to fork from that branch instead of
   the canonical's HEAD.
3. **Otherwise** → canonical = the source record's `Workspace`. If that
   directory is not a git repository, error with a clear message.

## Spawn semantics

Implemented as a `FromAgent` mode on `SpawnSpec`; the manager resolves the
source record and reuses the existing `spawnWorktree` flow with these
generalizations:

- **No owner/repo requirement.** Skip `EnsureCanonical` — the canonical
  already exists on disk. Run `git fetch` only when the canonical has an
  `origin` remote; a fetch-less spawn is valid for local-only repos.
- **Worktree path:** `<base>/.worktrees/<source-agent-name>/<branch-slug>`.
  Identical to today's layout for agents whose name matches the repo short
  name (chronicle → `.worktrees/chronicle/a11y`).
- **Agent name:** `<source-agent>-<branch-slug>` (matches the existing
  `chronicle-a11y` precedent). `--name` overrides; `-2`, `-3` suffixes on
  collision via the existing name reservation.
- **Inherited from the source record:** `Template` and `Env` (merged under
  any `--env` overrides). `Repo` is copied into the new record for
  bookkeeping when present. Model, harness args, and idle-suspend re-resolve
  from the template as in any spawn.
- **Branch creation:** unchanged — `AddWorktreeForBranch` handles existing
  local branch / remote tracking branch / new branch from `--base`
  (default: canonical HEAD).

## Cleanup

No changes. `stop --prune` and `agent prune` already operate on
`CanonicalPath` + `WorktreePath`, which are populated the same way.

## Error handling

- Source agent not found → error suggesting `leo agent list`.
- Source workspace not a git repo → explicit error naming the workspace.
- Branch already checked out in another worktree → git's own error surfaced.
- Fetch failures on remoteless repos are impossible (fetch skipped); fetch
  failures with a remote abort the spawn as today.

## Testing

- Unit: resolution matrix — owner/repo-backed source, plain-name source,
  worktree-agent source (canonical redirect), repo-less non-git source
  (error).
- Manager: spawn against a real temp git repo (no remote) — verifies the
  fetch-skip path and worktree creation; rollback on supervisor failure.
- CLI: arg wiring, flag validation, remote-host forwarding argv.
- `make e2e` before push (standing rule — config/argv changes).
