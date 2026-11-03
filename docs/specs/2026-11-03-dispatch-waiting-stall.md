# Dispatch waiting-stall: stale shell count + caller notice

**Status:** approved by Evan 2026-11-03 (scope: options A + notice + release-while-waiting + debug log)

## Problem

On 2026-11-03, an interactive dispatch (d-e27f0353134d) ended its turn while a background shell was still running. That shell could never finish. The dispatch sat in `waiting (1 shell)` for about 4 hours and its caller was never told. The caller then killed the shell. Leo kept showing 1 shell, and it refused both `leo_send_dispatch` and `leo_release`. Only `leo_cancel` got the caller out.

Root causes (from investigation):
- The background tracker (`internal/consult/pending_tracker.go`) prunes a task id only on:
  - a *successful* `TaskStop`/`KillShell` PostToolUse,
  - a `<task-id>` prompt,
  - a Stop that lists ids.

  A failed `TaskStop` (PostToolUseFailure) is ignored (`:43-45`).
- Even when the tracker does prune, `setTrackedLocked` (`:243-249`) neither recomputes the run's status nor clears the stale `Pending` on open turns. So a pruned run can stay `waiting`.
- `stalled` for waiting runs is read-only, with a 2 h cap (`interactive.go:29`). Nothing pushes a notice to the caller.
- `release` refuses every status other than idle or terminal (`release.go:25,39`).

## Changes

1. **Failed stop prunes.** A PostToolUseFailure for `TaskStop` or `KillShell` prunes the task id when the error says the task isn't running. That means not found, already exited, or no such task; match the error shapes Claude Code actually emits. Any other failure leaves the task tracked.
2. **Pruning recomputes.** Any prune (all four paths) recomputes the run's status and every open turn's `Pending`. Once nothing is pending, the run moves on exactly as it would have if the background work had finished normally.
3. **Waiting notice.** A run can be `waiting` with no hook activity for 30 minutes. When that happens, the caller gets one notice through the existing notification path, the same one the #276 close notice uses. It is sent only when the dispatch has notify on.
   - **Text:** names the dispatch id, the label, what it is waiting on (e.g. `1 shell`), and how long it has been quiet. It also says the caller can `leo_release` it, `leo_cancel` it, or keep waiting.
   - **Frequency:** one notice per waiting episode. Hook activity or a status change ends the episode, so a later quiet stretch can notify again.
   - **No state change:** the notice changes nothing. Waiting runs keep the 2 h `stalled` flag.
   - **Configuration:** the 30 min window is a named constant, not config.
4. **Release while waiting.** `leo_release` (and `leo release`) accept a `waiting` run. Its open turns end the way an idle release ends them: a turn that already produced its result keeps it, and a turn with no result is closed as released, not interrupted. The pane closes and background work in it dies with it. The refusal message for `leo_send_dispatch` on a waiting run now names `leo_release`/`leo_cancel`. It no longer claims the turn "will continue when it finishes".
5. **Debug log.** Every time the tracker keeps or prunes an entry, one daemon-log line records: dispatch id, hook/event name, task id, decision (kept/pruned) and reason. No prompt or tool contents.

## Non-goals

- The mod reporting Claude's live background task list on turn.complete. That is the authoritative reconcile, deferred to a separate spike.
- Detecting a shell killed with no event at all. The notice covers that case.
- Changing the 2 h `stalled` threshold or `leo_wait` semantics.

## Verification

- Go tests through public seams:
  - a failed `TaskStop` "not found" prunes, and another error doesn't;
  - a prune clears turn `Pending` and moves the run out of `waiting`;
  - the notice fires once at 30 min of quiet, using an injected clock, and re-arms after activity;
  - release of a waiting run finalizes its turns as specified;
  - the send-rejection text is updated.
- Replay the d-e27f0353134d shape as a fixture: a turn ends with 1 shell, children's turns arrive, then 4 h of quiet. Assert the notice arrives and release works.
- Live check on an isolated test daemon: dispatch a claude run that ends its turn with a never-ending background loop. Kill the loop with `TaskStop` and confirm the status clears. Release a second waiting run. The notice timing is covered by the fixture, not live.
