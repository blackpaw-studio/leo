# Interactive dispatches survive a daemon restart

Status: approved by Evan 2026-10-19.

## Problem
A daemon restart (`leo update`, `leo service restart`, a crash) kills every live interactive dispatch.

On 2026-10-19 at 09:04:48, four implement runs were cut off mid-tool-call and marked `error:"daemon restarted"`. Two of them were 6 minutes into their turn. Graceful restart never saw them, because it only probes supervised agents.

The processes did not die with the daemon:
- The `tmux -L leo` server outlives restarts (up since Oct 13).
- The claude side kept issuing tool calls, and the leo-bridge mod was reconnecting.

The new daemon itself killed them. On startup, `Dispatcher.MarkInterrupted` (`internal/consult/consult.go:1181`) calls `closeRestartPane`, then writes `TurnLost` and the `daemon restarted` error.

## Goal
A live interactive dispatch whose pane survives the restart is **adopted**, the way supervised agents are (PR #86): same run id, same open turn, and the caller is not notified of a close. The result is delivered normally when the turn finishes.

## Behaviour
1. **Startup.** For each interactive run that was live (not terminal) when the old daemon died:
   - **Pane present and certain:** found via the persisted pane id or `FindPaneByDispatchID`, with `panePresent` true. Do not kill it. Keep the run's status and open turn, and mark the run `adopting`. The status shows as the existing live status, so no wire change is needed unless one is unavoidable.
   - **Pane absent or uncertain:** settle as today (`TurnLost`, `daemon restarted`, a close notice to the caller).
2. **Re-keying.** When the run's leo-bridge mod reconnects, re-open the bridge key `dispatch.<id>` so its reports land.
   - Today the reconnect gets a 503 `bridge agent not open`.
   - Shell-hook reports for an adopted run are accepted as for a live run.
3. **Reconciliation.** Once adopted, reconcile anything missed while the daemon was down from the pane state, using the existing pane reconciler:
   - A turn that finished during the outage (pane idle, composer empty) is completed from the transcript, or falls back to `TurnLost` with a notice if no final text can be recovered.
   - Background-task tracking (#277) restarts empty, and the next Stop repopulates it.
4. **Bounded wait.** A run that has not re-attached its bridge or shown pane activity within `defaults.dispatch.adopt_timeout` (default 2m) is settled as today. Nothing hangs forever.
5. **Headless runs** are unchanged. They still settle as `daemon restarted`, and continuation already covers them.
6. **Graceful restart.**
   - The busy prompt lists live interactive dispatches separately: `N agents are working (...) and M dispatches are running (...)`.
   - Dispatches are not sent a pause message. Adoption makes that unnecessary.
   - `--force` and the drain timeout are unchanged.
7. **Observability.**
   - Log one service.log line per run (`adopted` or `settled: <reason>`).
   - The run record notes `adopted_at`.

## Out of scope
- Resuming interactive runs whose pane is gone, via a fresh `claude --resume`.
- Headless survival.
- Draining or pausing dispatches.

## Open facts for the planner to verify
- Whether hook reports are retried after a failed POST, or lost.
- Why two of the four records read `status: released` while also carrying the restart error.
- Whether the bridge mod's reconnect re-sends the launch token, so the new daemon can tell which run it is.
