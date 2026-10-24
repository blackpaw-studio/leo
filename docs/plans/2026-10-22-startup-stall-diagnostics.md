# Plan: daemon startup-stall diagnostics, Phase A

**Spec:** `docs/specs/2026-10-22-startup-stall-diagnostics.md`. If this plan and the spec disagree, the spec wins.
**Branch:** `feat/startup-diagnostics`. Commit once per step, using conventional commit messages.
**Tests:** run them with `env -u TMUX` (see memory: `$TMUX` hides exec seams that tests forgot to stub). Every new test package needs a `main_test.go` that calls `testenv.Main`. `internal/testenv/coverage_test.go` enforces this.

## How the daemon logs today
- The startup path writes with `fmt.Fprintf(os.Stdout/os.Stderr)`, and those lines have **no timestamps**. Other daemon code uses stdlib `log` with `LstdFlags` (`service/push.go:28`, `web.New`). `installLogRotator` dup2s fds 1 and 2 into `service.log`. Under launchd, `StandardErrorPath` points at the same file.
- New lines use `log.New(os.Stderr, "", log.LstdFlags).Printf`. It is built once in `defaultSupervisedExec` and injected everywhere else. Nothing calls the global `log`.

## Design decisions
- **Step timer:** a new leaf package `internal/startup`.
  - `Timer`: `New(now func() time.Time, logf func(string, ...any)) *Timer`, `Step(name) (done func())` (logs `startup: <name> took <dur>`), `Total()` (logs `startup: total took <dur>` since `New`), and `Logf` (a passthrough for detail lines).
  - Every method is safe on a **nil receiver**, so tests and callers that pass no timer don't change. No globals: `time.Now` is passed in only at the composition root (`defaultSupervisedExec`).
- **Call counting:** `startup.ExecCounter` (atomic) with `WrapContext(fn)` and `Wrap(fn)`. It classifies calls by `filepath.Base(name)` (`tmux`, `git`) and `Snapshot()` returns `{Tmux, Git}`. Web wraps the seams the restart path already uses, and the summary line logs the difference before and after each call.
- **Slow-record warning:** stays inside `consult`. It uses `d.now`, which is already injectable, and an unexported `slowRestartRecord time.Duration`, where zero means `defaultSlowRestartRecord = 2*time.Second`. There is no config knob, because the spec fixes 2 s. Tests set the field in-package. The logger arrives through `(*Dispatcher).SetStartupLog(logf)`; nil means no-op.
  - The warning fires **as each record finishes**, not at the end of the loop, so a long stall still shows partial progress.
- **SIGUSR1 handler:** lives in `internal/service/goroutine_dump.go`. It is installed right after `AcquireDaemonLock` and before tmux, the IPC socket, and `web.New`, so every later step can be dumped.
  - `signal.Notify` is **never `Stop`ped**. Once the handler is gone, later signals are dropped instead of hitting SIGUSR1's default action, which terminates the process.
  - The loop goroutine exits on a `done` channel closed by a `defer` registered **first**, so it runs last. Dumps therefore keep working while a stalled `srv.Shutdown` or other defer is running.
  - It only reads runtime state and writes one file, so it cannot race shutdown's state changes. The channel has a buffer of 1, which merges bursts, and one goroutine handles dumps one at a time.
- **Dump file:** `<home>/state/goroutines-<unix>.txt`, mode 0600, written with `pprof.Lookup("goroutine").WriteTo(f, 2)`.
  - The dump is first written to a dot-prefixed temp file in `state/`, then `os.Link`ed to the final name (which fails if the name exists), then the temp file is removed. Same-second collisions get `-2`, `-3`, and so on.
  - The final name only appears once the file is complete, and an earlier dump is never overwritten. It then logs `goroutine dump written to <path>`.
- **`leo service dump` safety:** the PID comes from `/health` over the IPC socket. The socket was up during the incident's stall, because `srv.Start` runs before `StartWeb`.
  - The PID is **not** taken from the lock file. A stale PID could point at a reused process, and SIGUSR1 would kill it.
  - `/health` gains an additive `goroutine_dump: true` field. It is set only through `daemon.WithGoroutineDump()`, so a new CLI never signals an older daemon, which would die from the signal.

## Steps
1. **`internal/startup` package.** Add `timer.go`, `execcount.go` and `main_test.go`.
   - Failing test first: `TestExecCounterClassifiesByBaseName`. Absolute `/opt/homebrew/bin/tmux` and `git` are counted, `sh` is not, and the wrapped command still runs the inner function.
   - The Timer gets **no unit test**: the spec calls the timing logs wiring, checked in Phase B.
   - Verify: `env -u TMUX go test -race ./internal/startup/`
2. **consult: slow records and counts.** Edit `consult.go` (`MarkInterrupted`, Dispatcher fields, `SetStartupLog`) and `spill_cleanup.go`.
   - Wrap each operation in the record loop with a per-record timer that tracks the slowest operation. Operation names: `viewer pane lookup` (`reconcileRestartViewer`), `worktree reconcile` (`reconcileCreating`), `pane presence`, `pane close`, `worktree cleanup`, `notification restore`, `interactive restore`, `settle`.
   - Warning format: `startup: MarkInterrupted: slow record <id> took <dur> (slowest: <op> <dur>)`.
   - `MarkInterrupted()` returns `RestartSummary{Records int}`. Adding a return value keeps all 23 existing call statements compiling.
   - `Dispatcher.SweepRunFiles()` returns `SweepSummary{Kept, Removed int}`. Changing `runFileCleaner.SweepRunFiles` to return the removed count means updating the `spillRuntime` fake in `spill_cleanup_test.go`.
   - Failing test first: `TestMarkInterruptedWarnsOnSlowRecord` in a new `restart_slow_test.go`. Use a FileRecorder in `t.TempDir()` with two terminal interactive records that have `PaneID`. A fake runtime's `PanePresence` moves a mutex-guarded fake clock forward 3 s for record A and 1 s for record B. Assert exactly one warning, naming A and `pane presence`, and that `Records == 2`. Reuse the PanePresence fake in `restart_adopt_test.go` or `interactive_test.go`.
   - Also extend `TestSweepRunFilesKeepsOnlyLiveRuns` to assert the returned counts.
   - Grep that every git call on the restart path goes through `d.git` / `GitCommand`, so the counts are complete.
   - Verify: `env -u TMUX go test -race -run 'TestMarkInterrupted|TestSweepRunFiles|TestDispatcherSweepRunFiles' ./internal/consult/`, then the whole package.
3. **web: sub-steps.** Edit `web.go` (`Options.Startup *startup.Timer`, `New`) and `consult_runtime.go`.
   - Time these steps: `web.New: client policies` (`SetClients`), `web.New: consult runtime` (all of `setupConsultRuntime`), with `web.New: MarkInterrupted` and `web.New: SweepRunFiles` nested inside it, and `web.New: setupChat`.
   - In `setupConsultRuntime`, create one `ExecCounter` and wrap `s.execCommandContext` once for both viewer and runtime. Set `s.consults.GitCommand = c.Wrap(s.consults.GitCommand)` and `s.consults.SetStartupLog(opts.Startup.Logf)`.
   - After each call, log `startup: MarkInterrupted: N records, T tmux calls, G git calls` and `startup: SweepRunFiles: kept K, removed R run files, T tmux / G git calls`.
   - Wiring: no unit test, verified in Phase B. Verify: `env -u TMUX go test -race ./internal/web/` stays green.
4. **daemon: timer pass-through and dump capability.** Edit `server.go` (`WithStartupTimer`, `WithGoroutineDump` options; `StartWeb`), `handlers_observe.go` (`healthData.GoroutineDump`) and `wait.go`.
   - `StartWeb` times `web.New`, `StartChat/Recover` and `listener bind`, and passes `Startup: s.startup` into `web.Options`.
   - Add `DumpTarget(ctx, workDir) (pid int, capable bool, ok bool)` with its own 5 s probe timeout. The current 1 s `probeTimeout` is too tight at load 50.
   - Failing test first: `TestHealthAdvertisesGoroutineDumpOnlyWhenEnabled` (the field is absent by default and true with the option).
   - Verify: `env -u TMUX go test -race -run TestHealth ./internal/daemon/`
5. **service: dump handler and step wiring.** Add `goroutine_dump.go`: `goroutineDumper{dir, now, logf}.dump() (string, error)` and `serveDumpSignals(sigs <-chan os.Signal, done <-chan struct{}, dump func())`.
   - Failing tests first (fake channel, fake clock, no real signal):
     - `TestDumpSignalWritesGoroutineFile`: the file name is `goroutines-<fake unix>.txt`, it contains `goroutine `, and the log line names the path.
     - `TestDumpSameSecondGetsSuffix`: two dumps in the same fake second produce `-2`.
   - Then wire `defaultSupervisedExec`:
     - Create the timer at entry.
     - `defer close(dumpDone)` runs before the lock (so it fires last). `signal.Notify(ch, SIGUSR1)` and the dump goroutine start right after the lock.
     - Steps: `lock`, `tmux server`, `observability`, `chat open`, `daemon wiring` (wireBridge through SetDrain, including the spill sweeps), `IPC listen` (`srv.Start`), `agent manager`, `web` (config.Load and StartWeb), `RestoreAgents`, then `Total()` after `MarkReady`.
     - Pass `daemon.WithStartupTimer(timer)` and `daemon.WithGoroutineDump()`.
   - The step logging is wiring and is verified in Phase B.
   - Verify: `env -u TMUX go test -race -run 'TestDump' ./internal/service/`
6. **cli: `leo service dump`.** Add `service_dump.go` and add it to the `cmd.AddCommand` list in `service.go`.
   - Dependency struct, following the `defaultDaemonRestartDeps` pattern: `target` (`daemon.DumpTarget`), `signal` (`syscall.Kill(pid, SIGUSR1)`), `glob`, `now`, `sleep`, and `timeout` (10 s).
   - Flow:
     1. Snapshot `state/goroutines-*.txt`.
     2. Probe `/health`.
     3. If the daemon is not reachable, exit non-zero with `daemon is not running (no answer on <sock>)`. Send nothing and use no lock-file fallback.
     4. If it is reachable but lacks the capability, exit non-zero with `daemon pid N (vV) predates 'leo service dump'; not signalled (SIGUSR1 would terminate it)`.
     5. Otherwise signal, poll until a new name appears, and print the path.
     6. If nothing appears by the timeout: `signalled pid N but no dump appeared in 10s; see 'leo service logs'`.
   - Failing tests first (fake deps and clock, no real signal or sleep): not running, not capable (assert the signal was never called), happy path, timeout.
   - Update `docs/cli/service.md`.
   - Verify: `env -u TMUX go test -race -run TestServiceDump ./internal/cli/`
7. **e2e: real signal.** Add `e2e/service_dump_test.go` using the `newInteractiveE2E` harness.
   - Run `leoBin service dump -c <cfg>`. The printed path must exist and contain `goroutine `. Socket `/health` must still return OK with the same PID. `serviceLog()` must contain `goroutine dump written to`.
   - After SIGTERM and Wait, `service dump` must exit non-zero with `not running`.
   - Verify (memory: never `make e2e` with a leaked env): `env -i HOME=$HOME PATH=$PATH TMUX_TMPDIR=$(mktemp -d) make e2e E2E_RUN=TestServiceDump`
8. **Integration gate.** Check the load average first; above ~30, defer the full runs.
   - Run `env -u TMUX make test`, `make lint`, then the full e2e in the `env -i` form above. Then do the fresh-context review (`review`; signal handling and process lifetime may warrant `review.concurrency`).

## Risks
- **Unbounded hang:** a step that never returns never logs its `took` line. The last `took` line and its timestamp name the step that is in progress, and the dump finds the exact location. This matches the spec's design.
- **Counts are approximate:** tmux/git counts are before/after differences over permanently wrapped seams. Adoption goroutines started by `restoreInteractive` can add a few concurrent calls. The roster and placement loops start only after `MarkInterrupted`, so they don't add to the count.
- **Lock-step line:** the `lock` line is written before the log rotator is installed. It reaches `service.log` only through launchd's `StandardErrorPath` or systemd's `append:`. Check this in Phase B.
- **Version skew:** a new CLI against an old daemon is guarded by the health capability. A launchd respawn reusing the PID between the probe and `kill` is a window of microseconds; accepted.
- **Dump cost:** `WriteTo(…, 2)` briefly stops the world. That is acceptable at about 1,400 goroutines.

## Open questions
1. **New `/health` field:** `goroutine_dump` is a plan decision the spec doesn't name. It's additive and `omitempty`, and it is the only safe way to gate the signal. Confirm the name, because leoterm also reads `/health`.
2. **No retention for dump files:** they build up in `state/` until removed by hand. The spec is silent on retention, so this plan adds none.
3. **Extra step:** `daemon wiring` is added beyond the spec's minimum list, so the gap between `chat open` and `IPC listen` is accounted for. The spec allows extra steps.
