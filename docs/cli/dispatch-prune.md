# leo dispatch prune

Remove the isolated worktrees of finished dispatches and report what is left.

## Usage

```bash
leo dispatch prune [--dry-run] [--json]
```

| Flag | Description |
|------|-------------|
| `--dry-run` | List every worktree with its action and size, and the total reclaimable, without changing anything. |
| `--json` | Print the report as one JSON object. A failure is `{"error": "...", "message": "..."}` with a non-zero exit. |

Run it with `--dry-run` first. It reads dispatch records from `~/.leo/state/dispatches/` and works on the disk directly, so the daemon does not need to be running.

## What it considers

Only dispatches that have **ended** (their worktree on disk, or a stale record of one). A running, idle or waiting dispatch, and one whose pane is still recorded, is listed as `keep:not-terminal` and never touched. The rules for the rest are the ones cleanup applies when a dispatch finishes (see [Worktree cleanup](../configuration/dispatches.md#worktree-cleanup)); they run against the dispatch's canonical repository.

| Action | Meaning |
|--------|---------|
| `remove` | Clean, and at its base commit or with every commit reachable from a ref other than its own `leo/*` branch. A checkout moved onto another branch (or detached) is judged the same way, with that branch excluded as well; it is never deleted. Removed with a plain `git worktree remove` (never `--force`; ignored build output does not block it). The `leo/*` branch is deleted only if its tip is reachable from another ref. |
| `keep:dirty` | Modified tracked files or untracked (non-ignored) files. |
| `keep:unmerged` | Clean, but has commits no other ref reaches (for a checkout on another branch, no ref besides that branch and `leo/*`). |
| `keep:unverifiable` | The rules could not be applied: git failed, the worktree is locked or unregistered, or a headless run was interrupted by a daemon restart. |
| `keep:deferred` | The daemon postponed this cleanup because a writer may be live (`worktree_state: deferred`), and prune cannot confirm the writer is gone: a headless run needs a recorded process group that is verifiably empty; an interactive run is confirmed only if it was released. |
| `keep:not-terminal` | The dispatch has not ended, or its pane is still recorded. |
| `missing-repo` | The canonical repository no longer exists. Reported, never deleted. |
| `stale-record` | The dispatch has ended and its record still claims a worktree, but the directory is already gone (removed by hand). Size 0. A real run marks the record removed, runs `git worktree prune` in its repository (skipped if the repository is gone), and deletes the `leo/*` branch only under the same merged-elsewhere rule. |
| `orphan` | A directory under `state/worktrees/` with no dispatch record. Reported, never deleted. |

A record already marked `removed`, and one whose dispatch has not ended, is not listed when its directory is missing.

## Output

The dry run prints one line per worktree (action, size, dispatch id, branch, reason), a count and size per action, and the reclaimable total:

```text
ACTION        SIZE     ID                BRANCH         REASON
remove        3.0 GB   d-74da6aba61c5    leo/ug-0ba     clean, commits reachable from refs/heads/main
keep:dirty    1.2 GB   d-9d73eaa28be3    leo/el-0b      uncommitted changes
...
keep:dirty: 15 worktrees, 1.4 GB
remove: 155 worktrees, 88.4 GB

dry run: nothing was removed; reclaimable: 155 worktrees, 88.4 GB
```

Sizes are allocated disk space. `--json` carries the same data: `items` (with `size_bytes`), `totals` per action, `reclaimable`, `reclaimed`, and `pruned_repositories`.

## Removing

Without `--dry-run`, each `remove` worktree is re-read and re-assessed immediately before it is removed, so a dispatch that came back to life since the listing is skipped. After removal the dispatch record is marked `removed`, and `git worktree prune` runs once in each repository that was touched. Per-worktree failures are reported in the item (`error`), not fatal.

Remote hosts (`--host`) are not supported: run it on the machine that owns the state.
