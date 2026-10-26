# Prune: release deferred worktrees when no process works inside them

Status: proposed, 2026-10-25. Option A, chosen by Evan in Leo Chat.

## Problem

`leo dispatch prune` keeps every `WorktreeDeferred` record whose writer it cannot prove is gone (`internal/consult/prune.go` `deferredWriterReason`):

- **Interactive:** stays deferred unless `Status == released`.
- **Headless:** stays deferred when `ProcessGroup <= 1`.

Evan's machine has 70 such worktrees (32 GB). 64 are interactive and closed, failed or canceled. 6 are headless and done with no pgid. All 70 ended days ago. None of their records holds a pane id or pgid that could be probed, so they stay forever.

## Design

A new check in `deferredWriterReason` releases these records. A deferred record stops blocking prune when **all** of these hold:

1. Its status is terminal.
2. `EndedAt` is set and at least `deferredQuietAfter` (24 h) before now.
3. A process working-directory scan succeeded.
4. The scan found no live process whose cwd equals the worktree path or lies under it.
   - Compare symlink-resolved paths on both sides.
   - Use the path prefix plus a separator, so `/wt/d-1` does not match `/wt/d-10`.

When a record qualifies, prune does not remove it outright. It moves on to the existing assessment. Dirty, unmerged and missing worktrees keep today's verdicts, so no uncommitted or unmerged work is ever removed.

Otherwise the record stays `keep:deferred` with a reason that names the cause:

- "ended < 24h ago"
- "process <pid> (<comm>) working in worktree"
- "cwd scan unavailable: <err>"

The existing interactive-released and headless-pgid-gone paths are unchanged. The new check runs only when those would have deferred.

### The cwd scan
- It runs once per prune invocation, lazily, only if at least one record reaches step 3. Results are shared across records.
- It sits behind an injectable seam (`processCwds func(ctx) ([]procCwd, error)`), with `procCwd{PID int; Comm, Cwd string}`.
- **darwin:** `lsof -nP -a -d cwd -Fpcn`, parsed from the field output.
  - lsof exits 1 when some processes are unreadable. Treat output that has at least one parsed entry as success.
  - No entries means an error.
  - It has a 30 s timeout.
- **linux:** `os.ReadDir("/proc")`, then `os.Readlink("/proc/<pid>/cwd")`, with comm from `/proc/<pid>/comm`. Per-pid permission errors are skipped. If `/proc` itself can't be read, that is an error.
- **Other OSes:** error, which keeps records deferred.
- Processes owned by other users may be invisible. That is acceptable, because dispatches run as the invoking user.

### Scope
- The `leo dispatch prune` CLI only. Daemon-side cleanup stays as it is.
- `--dry-run` shows the new reasons, and for released records shows the verdict they now reach.
- No config knob. `deferredQuietAfter` is a constant, overridable in tests.

## Accepted risk
A process writing into a worktree from a cwd outside it is not detected. Dispatched runs always start in their own worktree, so this is not a realistic workload.

## Testing
- Table tests for `deferredWriterReason` through the public prune entry point, with the fake `processCwds` and an injected clock. Cases:
  - quiet and clear (released)
  - a process inside the worktree
  - a process inside a sibling with a shared prefix
  - a symlinked worktree path
  - ended < 24 h ago
  - a scan error
  - an empty scan
  - interactive and headless
  - a scan run only once across many records
- Parser tests for the lsof `-F` output (including exit 1 with partial output), and `/proc`-reader tests against a fake directory.
- A live check: `leo dispatch prune --dry-run` on this machine, with before and after reason counts, run by the orchestrator.
