# Plan: dispatch waiting-stall fix

Spec: `docs/specs/2026-11-03-dispatch-waiting-stall.md`. Package: `internal/consult`. No real sleeps: every test drives `d.now` and calls `Sweep` or `ReportHook` directly. Fast loop per step: `go test -race -run '<Name>' ./internal/consult/`.

## Verified facts
- `trackToolEvent` returns early for any event other than `posttooluse` (`pending_tracker.go:43-45`). `interactive.go:904` already passes it `posttoolusefailure`.
- `setTrackedLocked` (`:243-249`) only stores and persists. Turn `Pending` comes from `withTrackedTasks` (`interactive_ids.go:368,471`) and is only cleared by `resumeWaitingLocked`, `resumeTurnLocked` or `clearPendingLocked`.
- The error strings in the Claude Code 2.1.317 binary (`strings ~/.local/share/claude/versions/2.1.317`), from the TaskStop `StopTaskError` table:
  - `No task found with ID: <id>`, optionally followed by `. Did you mean: …`
  - `<id> is not running (status: <s>)`
  - `Task not found`
  - Refusals that must stay tracked: `cannot stop itself`, `is owned by …`, `Unsupported task type: …`.
- No recorded hook payload holds a TaskStop failure, because records keep no hook payloads. The `error` field name comes from the hook docs, not from a capture, so the live check must confirm it.
- `~/.leo/state/dispatches/d-e27f0353134d.ndjson` no longer exists, so the fixture is built from the spec's description.
- `HookActivity` is `json:"-"` and is reset to the adopt time on restart (`restart_adopt.go:114`). `BackgroundTasks` is also cleared on adopt, but turn `Pending` survives.

## Steps
1. **Failed stop prunes** (`pending_tracker.go`: `trackToolEvent` plus a new `taskGoneError(msg string) bool`).
   - Accept `posttoolusefailure` only for `taskStopTools`. Prune only when `taskGoneError(str(p,"error"))` is true. Matching is case-insensitive on the substrings `no task found with id`, `task not found` and ` is not running (status:`.
   - Test `TestFailedTaskStopForGoneTaskPrunes` (table over the 3 shapes, TaskStop and KillShell): `BackgroundTasks` becomes empty.
   - Test `TestFailedTaskStopOtherErrorKeepsTask` (`is owned by`, `cannot stop itself`, empty error): the task is still tracked.
2. **Tracker decisions and debug log.**
   - Change `trackToolEvent`, `trackPrompt` and `reconcileStop` to return `([]BackgroundTask, []trackDecision{TaskID, Decision kept|pruned, Reason})`.
   - `setTrackedLocked(s, tasks, decisions, event)` writes one line per decision to `d.trackLog io.Writer`. It defaults to `os.Stderr` and is injected in tests.
   - Format: `dispatch <id>: tracker <event> task=<id> <kept|pruned> reason=<r>`. Allowed reasons are a fixed set (`stop-ok`, `stop-failed-gone`, `stop-failed-other`, `task-notification`, `stop-omitted`, `stop-listed`, `adopted`). No error text, command or prompt goes in.
   - Test `TestTrackerLogsDecisionWithoutContents`: after a failed TaskStop whose error embeds a marker string, the log has exactly 1 line with the id, event, `pruned` and the reason, and the marker is absent. A second case asserts `kept`.
3. **Pruning recomputes** (`setTrackedLocked` plus a new `prunePendingLocked(s, pruned int, wake bool)` in `interactive.go`).
   - For each open turn with `Pending`, set `Tasks["shell"] = max(0, shell-pruned)` and drop zero counts. A turn whose `Pending` empties closes `TurnFinished` through `closeTurnLocked(s, id, TurnFinished, "")`, which keeps its WaitText. That is what `finishKeyedLocked` does for a Stop that leaves nothing pending.
   - If any turn changed, call `recomputeStatusLocked`, persist `"status"`, and reset `idleSince` through the idle path.
   - The prompt path (`trackPrompt`) passes `wake=true`. It only lowers counts, so the existing `resumeWaitingLocked`/keyed resume still carries the turn on, as today.
   - Tests:
     - `TestPruneWhileWaitingClosesTurnAndLeavesWaiting`: Stop leaves 1 tracked shell, then a superseded Stop omits it. Status is `idle`, the turn outcome is `finished` with the Stop text, and `PendingWork` is nil.
     - `TestPruneKeepsOtherPendingKinds`: with a monitor also pending, status stays `waiting` and the summary reads `1 monitor`.
     - `TestTaskNotificationPromptStillResumesTurn`, run in both unkeyed and keyed mode: the turn is `running`, not closed.
4. **Send rejection text** (`interactive.go:754-756`).
   - New text: `dispatch is waiting on background work (<pending>) with no wake in sight to rely on; leo_wait on it, or leo_release / leo_cancel it`. Final wording is up to the implementer, but it must drop the "will continue" claim.
   - Test `TestSendToWaitingRunNamesReleaseAndCancel`: the error contains `leo_release` and `leo_cancel`, and does not contain `will continue`.
5. **Release while waiting** (`release.go:23,36`, plus a new `finalizeOpenTurnsForReleaseLocked`).
   - Add `StatusWaiting` to both allow-lists.
   - Under `d.mu`, before `state.releasing=true`, close every open turn:
     - A turn with a result (non-empty `WaitText` or `Text` from a Stop) gets `TurnFinished` and keeps its text.
     - Any other turn gets a new `TurnReleased TurnOutcome = "released"` (`record.go:55`). `turnNotificationStatus` maps it to `StatusReleased`.
   - Close turns directly, not through `closeTurnLocked`'s status recompute, because the run goes straight to released. Clear `Pending`, `PendingWork` and `BackgroundTasks`, release `SlotHeld`, and dequeue `Queued`.
   - Turn notices are recorded suppressed, since the caller asked for this (see open question 1).
   - CLI `leo release` calls the same `Release`. Check it with a grep, not a new test.
   - Tests:
     - `TestReleaseWaitingRunFinalizesTurns`: a turn with Stop text ends `finished` with its text kept, and a turn with no result ends `released`. Run status is `released`, the pane is killed (fake runtime `Kill` called with that pane id), and the slot is freed.
     - `TestReleaseStillRefusesRunning`.
6. **Waiting notice** (new `notify_waiting.go`, called from `Sweep`).
   - Inside `Sweep`'s run loop (`interactive.go:~505`), for a non-terminal run with `Status==StatusWaiting`:
     - `episode := max(rec.HookActivity, s.waitingSince)`. `s.waitingSince` is set in `recomputeStatusLocked` whenever the status changes into waiting.
     - When `now.Sub(episode) >= waitingNoticeAfter` (`const = 30*time.Minute`), call `addWaitingNotice(&s.record, episode)`.
   - The key is `<id>#waiting@<episode.Unix()>`, and the existing-key check dedups it as `addCloseNotice` does.
   - Suppression rules match the close notice: `!Notify`, no caller pane or harness, or a covering wait.
   - Message: `[leo] dispatch <id> (<label>) waiting on <Summary> · quiet <shortDuration> · leo_release or leo_cancel it, or keep waiting` plus `pointerSuffix`.
   - Make the other key consumers treat it as a non-turn notice, with a shared `isRunNoticeKey` covering `#closed` and `#waiting@`:
     - `deliveryMessage` (`notify_inline.go:42`) sends the header only, with no inline turn body;
     - `collectDelivered` (`:180`) must not release;
     - `hasUnresolvedTurnNotice` (`notify_close.go:127`);
     - the sort at `notify.go:254`.
   - No status or `stalled` change.
   - Tests:
     - `TestWaitingNoticeFiresOnceAfterQuiet`: at 29m nothing is recorded; at 30m one entry is `pending`; at 31m and 4h there is still 1.
     - `TestWaitingNoticeRearmsAfterActivity`: a hook at 40m, then a sweep at 70m gives a 2nd key.
     - `TestWaitingNoticeSuppressedWithoutNotify`.
     - `TestWaitingNoticeDeliveredHeaderOnlyAndDoesNotRelease`: the fake delivery's message has no inline delimiters, and the run is still `waiting`.
7. **Replay fixture** `TestWaitingStallReplay` (new `interactive_waiting_stall_test.go`):
   - Script: keyed turn #1 Stops with 1 tracked shell, then child user turns #2 and #3 submit and Stop, then the clock moves through 4h of quiet with Sweeps every 5m.
   - Assert: exactly 1 waiting notice delivered to the fake caller; `stalled` true at 2h; status still `waiting`; `Send` rejected with the new text; then `Release` succeeds and #1 ends `finished` with its text.
   - Variant: a failed TaskStop (`No task found with ID: b1`) during turn #2 means turn #1 closes and no notice is sent.

**Integration gate:** check the load average first, then run `heavy make test`, `make lint`, and `env -i HOME=$HOME PATH=$PATH TMUX_TMPDIR=$(mktemp -d) make e2e`, all in the background. CI tmux is 3.4, and nothing here touches tmux argv.

## Notice scheduling and state
- **Timer:** the existing `Sweep` housekeeping tick, which already feeds `SweepNotifications` through its `defer`. There is no new goroutine or ticker. Notice latency is one sweep interval.
- **Episode state:** derived, not stored. The episode starts at `max(HookActivity, waitingSince)`. Any hook moves `HookActivity` and any re-entry into waiting moves `waitingSince`, so both re-arm without reset code.
- **Delivery state:** the notice entry lives in `Record.Notifications`, which is persisted, so a claimed or pending notice survives a restart through the existing restore and redeliver path.
- **Why the episode itself needn't survive a restart:** adoption sets `HookActivity` to now, so the daemon's own quiet clock starts over. At most one extra notice per restart, and only after 30 more quiet minutes. That is the spec's rule applied to what the daemon can observe.

## Release-while-waiting turn finalization
This is step 5. The text a turn keeps is its `mergeWaitText` result. `TurnReleased` is a new additive value on `/api/v1` turn objects. External users exist, so it gets a changelog line and needs no migration.

## Live check (isolated daemon, see memory `reference_isolated_leo_test_daemon`)
1. `make build`. Start `LEO_HOME=$(mktemp -d)` with its own `web.port`, then run `bin/leo service` in the background. Never `leo service restart`.
2. Dispatch a claude `explore` with: "run `while true; do sleep 5; done` with Bash run_in_background, then end your turn". Confirm `waiting (1 shell)`.
3. Kill the loop's process from outside, then `leo_send_dispatch` "TaskStop <id>". The tool should fail with a gone error. Confirm in the test daemon's log that the tracker line shows `pruned reason=stop-failed-gone`, and that status leaves `waiting`. Record the `error` text and field name from that log as the fixture's ground truth.
4. A second dispatch, same prompt: `leo release <id>` while it is waiting. The turn ends `finished` with its text and the pane is gone.
5. Kill the test daemon by PID, then `leo_release` both runs.

## Risks
- **Field name:** if Claude's failure field isn't `error`, step 1 is inert. Live check step 3 catches that, and the test table should cover only the field name confirmed there.
- **Over-counting:** decrementing shell counts can't tell a tracked shell from an untracked one counted by a counts-only Stop. The floor of 0 plus decrementing only by the pruned count keeps it from under-counting below the tracker.
- **Notice routing:** the new notice key must be routed in every `Notifications` consumer. `collectDelivered` matters most: releasing on delivery would kill a live run. `TestWaitingNotice…DoesNotRelease` guards it.
- **Closing on prune:** a waiting turn closed by prune while a late task notification is still in flight gets reopened as a new turn by that notification. This matches today's behaviour for a late wake after close.

## Open questions (spec didn't settle)
1. On release of a waiting run, should turn notices be suppressed (proposed, since the caller acted), or sent?
2. Should a waiting notice be suppressed when a `leo_wait` covers the run (proposed, matching #276), even though waits only report `stalled` at 2h?
3. Is `TurnReleased` acceptable as a new outcome value, or should turns with no result reuse `lost` with "released" text?

## Orchestrator decisions
- OQ1: suppress the turn notices on release of a waiting run, because the caller already acted.
- OQ2: suppress the waiting notice while a `leo_wait` covers the run, matching #276.
- OQ3: add the new `TurnReleased` outcome value. It is additive on /api/v1 and needs a changelog line.
- Live-check fix: step 3 can't use `leo_send_dispatch` on a waiting run, because the send is rejected. Instead, have the dispatch do it all in its own turn: start a background loop, `kill` its pid from Bash, call TaskStop on that id (expect the gone error), then start a second never-ending loop and end the turn. Expected result: the first task is pruned, and the run waits on exactly 1 shell.
