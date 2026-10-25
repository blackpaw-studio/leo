# Plan: startup reconcile off the critical path (Phase C)

**Spec:** `docs/specs/2026-10-22-startup-stall-diagnostics.md`, section "Phase C — fix (addendum 2026-10-24)". The spec wins if this plan conflicts with it.
**Branch:** `fix/startup-reconcile-offpath`. Commit after each step. Each verify command runs with `env -u TMUX`.

## Design

### Per-record operations: fast pass vs. background pass
The fast pass is `MarkInterrupted`, before bind. The background pass is `ReconcileRestart`, after `MarkReady`.

| Operation | Records non-terminal at shutdown | Records terminal at shutdown | Why |
|---|---|---|---|
| Viewer reconcile (`reconcileRestartViewer`, one tmux call) | fast | bg | Adopting a split interactive run needs its `PaneID`. For terminal records it only feeds viewer cleanup. |
| `reconcileCreating` (`worktree list`) | fast. One memoized list per `RepositoryRoot` per pass, with the 30 s timeout | bg (creating implies running, so this case is unexpected) | Its verdict feeds the settle write. A headless `Deferred` record is never re-assessed by the daemon, so moving this to bg would change the end state. At 3,000 records, 150 unmemoized calls would cost 13–33 s. The memo is exact because nothing mutates worktrees pre-bind. |
| Adopt (`restoreInteractive` → `ProbeAdoption`, bridge-key reopen) | fast | — | Keys must reopen before `RestoreAgents`/`Hub.AwaitAdoption`. Send and wait need the live run in `d.runs`. |
| Settle (headless block in `markInterruptedRecord`, `settleRestarted`, `addRestartCandidates`, write) | fast | — | List, wait and output must never see a dead daemon's `running`. |
| Pane presence and pane close | fast, inside `settleRestarted`, for refused adoptions only (few records) | bg | About 15% of records carry a pane at 2 tmux calls each, and nothing reads them before ready. |
| Worktree cleanup *decision* (pre-git checks of `cleanupWorktree`) | fast | bg | It must run before the notification restore puts the run in `d.runs`. Otherwise a headless run that has state skips the `deferCleanup` branch and gets removed, which is a different end state. |
| Worktree assessment and removal (`assessWorktree`, `worktree remove`, `branch -D`, `worktree prune`) | bg (queued) | bg | At about 1.8 git calls per record, this is the stall. |
| Notification restore (`restorePendingNotifications`) | fast (no exec, keeps delivery prompt) | bg, last op as today | Running it after pane close means `d.runs` never holds a stale `PaneID`. The queue runs records with unresolved notifications first. |
| `SweepRunFiles` | bg, first job | | Filesystem only. Its keep set is evaluated per file under `d.mu` (see step 4). |

Terminal records cost the fast pass only `Load` and an enqueue. They make zero exec calls.

### Background job rule
- Each job is `{id, kind: terminal | assess, owner *runState}`, where `owner` is captured in the fast pass and is nil for disk-only records.
- A worker takes `serialLocks([]string{id})`. It skips the job if `d.runs[id] != owner`, because a post-ready path now owns the run and its own terminal transitions re-run cleanup.
- Otherwise it re-reads the record through `lookup`. It skips an `assess` job whose state is already `Removed`.
- A `terminal` job runs the old terminal sequence on that fresh record, in the same order as before.

### API callers before a record is reconciled
- **List, Get and the roster** read disk. Status is already terminal and correct. `worktree_state` (present/kept/deferred) truthfully means "not removed yet". Viewer fields may be stale, but they are display-only, roster rows only show inside the grace window, and they converge when the pass ends.
- **Output** reads the stream and is unaffected.
- **Wait, Collect and `collectRun`** take the same serial lock and call `cleanupWorktree`. Whichever runs second re-reads and sees the first one's result. `Removed` makes the second a no-op, and `Kept` makes it re-assess. Release, close-finished and notify also serial-lock.
- **In-daemon `FileRecorder.prune`** (viewer tick) is held back until the pass completes. It could delete a split record with no worktree mid-reconcile, and the job's write would then bring the record back.
- **`viewer.Sweep`** receives the records minus the pending ids.
- **`leo dispatch prune`** is the CLI calling `PruneWorktrees` on disk. Its behaviour is unchanged. It already races daemon cleanup today, including during the old 11-minute stall. Both sides assess before removing and never force. The worst case is a record marked `Kept` for a worktree that is already gone, which the next evaluation turns into `Removed` or `stale-record`.

### Lifecycle
- `MarkInterrupted` stores the queue and the pending set on the `Dispatcher`.
- `Dispatcher.ReconcileRestart(ctx, workers)` blocks and returns a `RestartReconcileSummary`: records, skipped, removed/kept/deferred, git calls, timeouts, swept kept/removed, took, canceled.
- The fixed worker count is `restartReconcileWorkers = 4`. On `ctx.Done()` the pass stops dequeuing, and unprocessed records stay exactly as loaded.
- `web.Server.StartRestartReconcile()` is guarded by `sync.Once`. It launches a goroutine on a ctx derived from the consult `loopCtx`, so `stopConsultLoop` cancels the pass and also waits on its `done` channel. Web `Shutdown` therefore returns only after the pass has exited.
- `daemon.Server.StartRestartReconcile()` forwards to the web server when one exists. `process.go` calls it right after `srv.MarkReady()`.
- The pass logs one summary line through `opts.Startup.Logf`:
  `startup: restart reconcile: N records (S skipped), worktrees removed R / kept K / deferred D, G git calls (T timed out), ~M tmux calls, run files kept/removed a/b, took X[, canceled]`
  - The git count comes from the restart git func's own counter, so it is exact. The tmux count is a delta on the shared `ExecCounter`, which includes roster traffic, so it is approximate.
  - Slow records reuse `warnSlowRecord`.

### The 30 s git timeout
- It lives in the new file `internal/consult/restart_reconcile.go` as `const restartGitTimeout = 30 * time.Second`. A `Dispatcher.restartGitTimeout` field overrides it in tests, like `slowRestartRecord`.
- `d.restartGit(ctx) gitFunc` wraps each call in `context.WithTimeout`. It runs through a new `execGitContext(ctx, d.GitCommand, args...)` in `worktree.go`, which does Start, waits, and kills on `ctx.Done`, with `cmd.WaitDelay` set. The `GitCommand` seam keeps its signature.
- Read-only calls (list, status, `for-each-ref`, rev-parse) use the pass ctx. Mutating calls (remove, `branch -D`, prune) use `context.WithoutCancel(ctx)` plus the timeout, so shutdown never cuts a remove mid-flight. Shutdown waits for at most one mutating call per worker, up to 30 s.
- Non-restart callers keep `d.git` with no timeout. `worktree add` on a large repo must not be capped.
- A timeout yields `verdictUnverifiable`, so the worktree is kept. For `reconcileCreating` it yields `Deferred`. It logs `restart reconcile: git timed out for <id> (<args>): worktree left in place` and never deletes anything.

## Steps

1. **Restart-path git timeout.**
   - Changes in `worktree.go`: add `execGitContext` and `restartGit`. `applyWorktreeAssessment` takes `(assess, mutate gitFunc, rec, state)`, and `reconcileCreating` takes a lister. Extract `cleanupPrecheck(rec, state) (rec, needsAssessment)` from `cleanupWorktree` with no behaviour change. `MarkInterrupted` is still synchronous at this point and uses `restartGit(context.Background())`.
   - RED first, in `internal/consult/restart_reconcile_test.go`:
     - `TestMarkInterruptedGitTimeoutKeepsWorktree`: the seam returns `sleep 5` for `status`, the timeout is 50 ms, and the worktree uses a real temp repo. The dir and branch stay, the record ends `Kept`, and one timeout line is logged.
     - `TestReconcileCreatingTimeoutDefers`.
   - Verify: `env -u TMUX go test -race -run 'GitTimeout|CreatingTimeout' ./internal/consult/`

2. **Characterization fixture (the guard for every later step).**
   - `TestRestartReconcileEndState` uses a real git repo in `t.TempDir` and the existing fake interactive runtime. It has about 12 records, one per shape:
     - Terminal records: clean at base → Removed. Dirty → Kept. Unmerged → Kept. Interactive with a live pane → pane killed, then cleanup. Interactive with an uncertain pane → Deferred. Split viewer found / not found. Pending notification → ends in `d.runs`.
     - Creating present / creating absent.
     - Running headless → Failed + Deferred. Running interactive with no pane → `settleNoPane` + cleanup.
     - Spill files for live and dead ids.
   - A `restartEndState(t, d)` helper normalizes each record: status, worktree state, `branch_tip != ""`, pane and viewer ids, viewer kind, error, notification dispositions. It also captures worktree dirs, `leo/*` branches, spill files and `d.runs` keys.
   - The test captures today's values as literals. It passes on this commit by design, as a characterization test. Its RED comes in step 3.
   - Verify: `env -u TMUX go test -race -run TestRestartReconcileEndState ./internal/consult/`

3. **Split the fast pass and add a sequential `ReconcileRestart`.**
   - Files and functions: `consult.go` `MarkInterrupted`/`markInterruptedRecord` (terminal records go to `enqueueRestart`). `restart_adopt.go` `persistRestartSettle` becomes write → `cleanupPrecheck` → enqueue `assess` if needed → notification restore. Add `ReconcileRestart` with one worker to `restart_reconcile.go`.
   - RED first: `TestMarkInterruptedNoExecForTerminalRecords`. A counting `GitCommand` and tmux seam give 0 git and 0 tmux calls for terminal records, and exactly 1 `worktree list` for 3 creating records in one repo.
   - Update step 2's test to call `MarkInterrupted` and then `ReconcileRestart(ctx, 1)`. It fails until the queue is drained.
   - Migrate existing `MarkInterrupted` tests that assert terminal-record effects (for example `TestMarkInterruptedClearsKilledPaneAndReappliesLayout` and the ones in `notify*_test.go`) to a `markInterruptedAndReconcile(t, d)` helper. Find them with `grep -l 'MarkInterrupted()' internal/consult/*_test.go`. Tests that assert fast-pass effects stay as they are.
   - Verify: `env -u TMUX go test -race -run 'MarkInterrupted|RestartReconcile' ./internal/consult/`

4. **Workers, cancellation, sweep and summary.**
   - Four workers. Records with unresolved notifications go first. Jobs follow the ownership and serial-lock rule.
   - `SweepRunFiles` runs first and takes a `keep func(id) bool` evaluated under `d.mu` per file, in `spill_cleanup.go` `sweepGlob`. This is safe because a run registers in `d.runs` before its spill is written. The implementer confirms that order in `Start` → `interactive_runtime.go:203`. The function returns the summary.
   - RED tests:
     - `TestReconcileRestartBoundsConcurrency`: a blocking git seam sees at most 4 in flight.
     - `TestReconcileRestartStopsOnCancel`: it returns `Canceled`, and unprocessed records are byte-identical on disk.
     - `TestReconcileRestartKeepsSpillOfRunStartedDuringPass`.
     - `TestReconcileRestartSkipsRunOwnedAfterFastPass`.
   - Verify: `env -u TMUX go test -race -run 'ReconcileRestart|RestartReconcile' ./internal/consult/`

5. **Wiring.**
   - `internal/web/consult_runtime.go`: remove the synchronous `SweepRunFiles` step, add `StartRestartReconcile`, make `stopConsultLoop` wait on the pass, filter pending ids out of `viewer.Sweep`, and skip `Prune` until the pass is done. Keep the `web.New: MarkInterrupted` timing line. It now times the fast pass.
   - `internal/daemon/server.go`: add `StartRestartReconcile`.
   - `internal/service/process.go`: call it after `srv.MarkReady()`.
   - RED tests in `internal/web`:
     - `TestShutdownCancelsRestartReconcile`: a blocking git seam, then `Shutdown` returns and the goroutine has exited.
     - `TestConsultLoopHoldsPruneUntilRestartReconciled`.
   - Verify: `env -u TMUX go test -race -run 'RestartReconcile|ConsultLoop' ./internal/web/ ./internal/daemon/`

6. **`lock` log fix** (wiring only, no unit test, per Phase A).
   - In `defaultSupervisedExec`, call the lock step's `done()` after `installLogRotator`. Its duration still measures the lock and also includes the microseconds of dump, locale and rotator setup, which a comment notes.
   - On a lock failure, call `done()` before returning. That line goes to stderr only, so a losing daemon still never touches the winner's log.
   - Verify: `go build ./... && go vet ./internal/service/`. The repro's lock-line counter must report `in state/service.log: 1`.

7. **Repro tooling.**
   - `scripts/stall-repro/run.sh` gets a `WAIT_FOR_LOG=<regex>` option: after ready, poll `state/service.log` for the pattern before teardown, under the existing `MAX_WAIT`.
   - Add `scripts/stall-repro/snapshot.sh <T>`. It emits sorted JSON with:
     - per record, via `jq -S`: id, status, `worktree_state`, `branch_tip != null`, `pane_id`, `viewer_pane_id`, `viewer_kind`, error, and notification dispositions;
     - `git -C $T/repo worktree list --porcelain` paths with the `$T` prefix stripped;
     - `git branch --list 'leo/*'`;
     - `ls state/settings`.
   - Timestamps and commit hashes are dropped. `gen.py --seed 1` makes the record set deterministic.
   - Verify: `bash -n scripts/stall-repro/*.sh`

8. **Final verification.** Isolated LEO_HOME, `TMUX_TMPDIR` and port only. Never production, never `leo service restart`.
   - Build the old binary at `f8347e10` into the scratchpad as `leo-old` from a detached `git worktree`. Build the new one with `make build`.
   - Wrap every run in `env -i PATH="$PATH" TMPDIR="$TMPDIR"` so `LEO_DISPATCH_ID` and `TMUX` don't leak into the test daemon. Use distinct ports, 18371 and 18372.
   - **Equality run.** Both binaries at 3,000 records with 0 burn processes:
     - old: `LEO_BIN=…/leo-old MAX_WAIT=1800 run.sh 3000 0 18371`
     - new: `WAIT_FOR_LOG='restart reconcile:' run.sh 3000 0 18372`
     - Then run `snapshot.sh` on each `$T` and `diff`. The diff must be empty.
   - **Timing run.** New binary at 3,000 records with enough burn to reach load ≥50, using `WAIT_FOR_LOG`.
     - `ready after` must be under 5 s, and the `web UI listening` timestamp must also be under 5 s from start.
     - The summary line must show 0 timeouts.
     - Its snapshot must equal the equality run's old snapshot, except ids logged as timed out, of which 0 are expected.
   - **Integration gate.** Check the load average first. If it is above 30, defer:
     - `env -u TMUX make test && make lint`
     - `env -i PATH="$PATH" HOME="$HOME" TMUX_TMPDIR=$(mktemp -d) make e2e`
   - Review with `review.concurrency` (worker pool, serial locks, record-write races) at the P0/P1 bar.

## Risks and open questions
- **Notification latency.** Pending notifications on records that were terminal at shutdown are delivered after their job runs, not at bind. Queue ordering keeps that delay to about the first jobs of the pass. Open question: is that acceptable, or should those records' whole sequence run in the fast pass? That would cost their tmux calls before ready.
- **Lost updates.** Writers that bypass the serial lock and act on terminal records could race a job's write. The viewer path is filtered and prune is gated. The implementer must audit the remaining `writeRecord`/`PersistRecord` callers and list any left over in the PR as accepted risks.
- **Killed mutating git.** A `worktree remove` killed by the 30 s timeout can leave a partial checkout. The record stays not-`Removed`. A later assessment may then report it dirty and keep it. Nothing committed is lost, because removal only runs for clean trees whose commits are reachable from another ref.
- **Shutdown delay.** Shutdown can wait up to 30 s, worst case one mutating git call per worker.
- **Fast-pass cost.** The fast pass still reads all 3,000 JSON records through `Load`. If that alone approaches 5 s under load, report it rather than widen scope. Retention is out of scope.
- **Test migration.** Migrating the existing `MarkInterrupted` tests could mask a regression. Step 2's literal end-state fixture is the guard. Don't loosen it.
- **Approximate tmux count.** The tmux count in the summary includes concurrent roster calls, and its label says so.
