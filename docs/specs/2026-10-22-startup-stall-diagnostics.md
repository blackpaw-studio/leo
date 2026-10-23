# Daemon startup stall: diagnostics, then repro

**Status:** draft · 2026-10-22

## Problem
After the v0.58.0 update, the daemon's startup on PID 403 stalled for about 11 minutes. The
stall sat between the last `dispatch …: adopted` line and `web UI listening` (`service.log`
35969→35972). During that window `/health` reported `ready:false`, `:8370` was down, and agents
were not restored. The daemon had no child processes, and the host load average was about 50.
Startup log lines carry no timestamps, so the stall's duration and location were never measured.

The explore pass (no proof yet) ranked the suspects:
1. `Dispatcher.MarkInterrupted` (`consult.go:1205-1260`), called synchronously from `web.New`.
   For each record it makes tmux calls (`PanePresence`, `FindPaneByDispatchID`, each capped at
   5 s). For records in `WorktreeCreating` it runs git through `reconcileCreating` with **no
   timeout** (`worktree.go:256-260`). The work is O(records); the roster logged 1109 records
   before a prune cut them to 303.
2. `chat.Recover` → the publish/push chain (`chat/service.go:137-163`), which also runs before bind.
3. Starvation across the whole host making every fork slow. If this is the cause, #1 is how it shows up.

#292 is ruled out. Its startup-path code is pure or touches only the local filesystem.

## Phase A — observability (this PR)
1. **Startup step timing.** Each step from lock acquisition to `MarkReady` logs one line:
   `startup: <step> took <dur>`. The line uses a timestamped `log` format like the rest of the
   daemon's logs. The minimum set of steps is: lock, tmux server, observability, chat open, IPC
   listen, agent manager, web.New, its sub-steps (client policies, consult runtime /
   MarkInterrupted, SweepRunFiles, setupChat), StartChat/Recover, listener bind, RestoreAgents,
   and a total. This uses an injected clock rather than a global `time.Now`.
2. **MarkInterrupted / SweepRunFiles detail.** These log the number of records processed, the
   number of tmux and git calls they make, and a warning for any single record that takes over
   2 s (dispatch id and operation).
3. **Non-fatal goroutine dump.** On SIGUSR1 the daemon writes `pprof.Lookup("goroutine")` at
   debug=2 to `state/goroutines-<unix>.txt` and logs the file path to `service.log`. It does not
   exit or change state. This file goes beside the log instead of inside it, so a 1,400-goroutine
   dump doesn't flood the log rotation. A `leo service dump` command sends the signal and prints
   the path. No pprof HTTP endpoint is added (out of scope).
4. **Tests.** SIGUSR1 produces a file containing `goroutine` and leaves the daemon serving
   `/health`. MarkInterrupted's slow-record warning fires with a fake clock or a fake tmux seam.
   The timing logs are wiring, so they get no unit test; their check is the live repro in Phase B.

## Phase B — repro (after A merges; not production)
- Run an isolated test daemon with its own `LEO_HOME` and port, **and** its own tmux socket
  (`TMUX_TMPDIR=$(mktemp -d)`). The separate socket keeps copied dispatch records from touching
  production panes.
- Generate about 1,100 synthetic dispatch records. Shape the mix like production: terminal
  interactive records with panes, split-viewer records, and some in `WorktreeCreating`.
- Load the host to ≥50 with CPU-burn processes. Measure the startup step timings, and use
  `leo service dump` mid-stall.
- Exit criterion: either the stall is reproduced with a stack showing its location, or it is
  shown not to reproduce at 1,100 records under load. Either result is reported to Evan.
- No production restart. If a dump from PID 403 is ever wanted, ask Evan first.

## Phase C — fix
This phase gets its own short spec addendum once Phase B names the cause. Likely candidates,
**not** in this PR:
- Move `MarkInterrupted` off the pre-bind path (bind first, reconcile in the background).
- Bound `reconcileCreating`'s git calls with a context timeout.

## Out of scope
- The attach-before-restore UX (separate spec).
- Prune/deferred-worktree confirmation (Rocket's follow-up, which is pending Evan).
