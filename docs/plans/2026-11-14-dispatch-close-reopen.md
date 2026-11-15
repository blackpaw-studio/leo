# Plan: Dispatch panes close when done; follow-ups reopen them

Source of truth: `docs/specs/2026-11-14-dispatch-close-reopen.md`. Repo: `/Users/evan/.leo/agents/leo`, branch `feat/dispatch-close-reopen`. Commit once per step.

## Answers to the brief's questions

**1. What release does today** (`releaseLocked`, `internal/consult/release.go:70-180`)
- It only accepts interactive runs whose status is idle, waiting, done, failed, canceled, timeout or closed (`release.go:17`). A waiting run's open turns end first (`:34-68`).
- It kills the pane through a kill operation queued with the run's other pane operations (`:106-129`). It then sets `StatusReleased`, clears `PaneID` and the viewer fields, and sets `EndedAt` and `ReleasedOnFinish` (`:130-145`). It persists the record, closes the record handle and closes `done`.
- `releaseRunFilesLocked` deletes the run's MCP config, settings spill file and brief file (`:171`).
- Worktree runs go to `cleanupWorktree` (`:176`, `worktree.go:154-244`):
  - **removed**: the tree is clean and either still at its base commit or its commits are merged into another ref. The merged branch is deleted too.
  - **kept**: dirty, unmerged, or unverifiable.
  - **deferred**: a writer may still be live.
- The record stays on disk. Disk retention keeps the newest 20 records (`RecordsKept`, `fsrecorder.go:22`), but never drops a worktree record whose worktree hasn't been removed.
- A follow-up to a released run gets a specific error only if `ReleasedOnFinish` is set (`release_on_finish.go:65`). Otherwise it falls through to a generic error.
- Today the pane close itself also cleans up the worktree. `closeRecordedPaneThen` runs the cleanup when the run is terminal and has no pane (`pane_cleanup.go:92`), and so does `collectRun` (`consult.go:1082-1106`).

**2. Where `release_on_finish` is defaulted per role**
- The default is set in `ResolveReleaseOnFinish` (`release_on_finish.go:16`) as `config.IsReadOnlyRole(role)`.
- It's resolved into the record at `consult.go:474`, for interactive dispatch runs only.
- The wire inputs are the MCP parameter (`internal/mcp/tools.go:552`, schema text at `:779`) and the web handler (`internal/web/handlers_consult.go:33`).

**3. How the 1h close builds its notification**
- `Sweep` (`interactive.go:1306`) looks at idle runs. It closes one when `idleSince` is at least `idleCloseAfter` old (1h, `:23`) and the composer is empty (`:1393`).
- The close path is `beginCloseLocked` → settling → `finishInteractiveLocked(StatusClosed)` (`:1227`) → `closeNoticeLocked` → `addCloseNotice` (`notify_close.go:65`).
- The header reads `[leo] dispatch <id> (<name>) closed: idle 1h · active … · last turn … · run is closed; follow-ups will be rejected — collect with leo_wait`.
- The notice is suppressed when notify is off, there's no caller, or a `leo_wait` covers the run. It's held until the run's turn notices have resolved (`:108-129`).
- On delivery, `deliveryMessage` (`notify_inline.go:42`) calls `closeNoticeMessage` → `inlineNotification(rec, lastTurnID)`. That pastes the last turn's **whole result again**, which is the duplicate paste the spec complains about.

**4. Session resume support**
- **Interactive launch**: the launch spec at `interactive_runtime.go:158` sets no `Session`, so every interactive dispatch starts a fresh session.
- **Claude**: `Args` emits `SessionArgs(spec.Session)` for every launch kind (`harness/claude/args.go:103`, `claude.go:127-136`). Setting `spec.Session{Mode: SessionResume, ID}` therefore yields `--resume <id>`. The opening message already travels as a positional argument read from a file (`interactive_runtime.go:173`), so a reopen launch becomes `claude --resume <id> "<msg>"`.
- **Codex**: `SessionArgs` gives `resume <id>` (`codex.go:72-77`), but the TUI argv leaves it out. Only the agent supervisor adds it, through the tmuxtui `RefreshSessionArgs` (`codex/driver.go:55-64`, `harness/driver.go:99`). The dispatch path never calls that.
- **Opencode**: `-s <id>` exists, but interactive dispatch rejects opencode entirely (`interactive.go:186`), so it's out of scope.
- **Checks**: nothing today checks that a transcript exists or that the account matches, and headless continuation doesn't either (`continuation.go:31-120`).
- **Session id capture**: the id is taken from the hook payload, but only when the record has none yet (`interactive.go:901`).

## Design: pane closed is separate from run released

- **Pane closed (parked)**: `Status == closed`, `PaneID == ""`, `Retained == true`.
  - The record, `SessionID`, `TranscriptPath`, cwd and worktree are all kept.
  - The run's terminal status still means "no live process": `done` is closed and no slot is held.
  - `Retained` means "nothing may be cleaned up; a follow-up may relaunch".
- **Run released**: `Status == released`. The worktree goes through cleanup under the existing safeguards, and every later follow-up is rejected.
- **New record fields** (all additive with `omitempty`; old records read as not retained):
  - `Retained bool` (`retained`)
  - `ResumeError string` (`resume_error`)
  - `ReleaseReason string` (`release_reason`: `manual` | `retention`; the legacy `ReleasedOnFinish` is still honoured when reading)
  - `Turn.ResultCollectedAt time.Time` (`result_collected_at`)
- **Predicates** (`record.go`):
  - `parked(rec) = rec.Mode == interactive && rec.Status == closed && rec.Retained`
  - `resumable(rec) = parked(rec) && rec.SessionID != "" && rec.ResumeError == "" && harnessResumesInteractive(rec.Harness)`
- **Runtime-only flag**: `runState.reopenUnconfirmed`. It's set when a reopen is admitted and cleared when the harness reports the expected session id.
- `release_on_finish` keeps its name and JSON field (D10). Its meaning becomes "close the pane after each delivered result, so a follow-up can reopen it". It defaults to true for every interactive dispatch, and an explicit `false` is the opt-out.

### State machine

| # | From | Event | To | Side effects |
|---|---|---|---|---|
| 1 | new | dispatch | queued or live | as today |
| 2 | live (idle) | the **last** turn came from the orchestrator, finished, and its complete result was collected (inline delivery or ack, or `leo_wait` without truncation); `release_on_finish` on; no typed turn (D4); nothing queued, pending, or awaiting permission or a slot | **parked** | kill the pane; set `ResultCollectedAt`; set `EndedAt`; close the handle; drop run files; **no** worktree cleanup; **no** close notice |
| 3 | parked | `leo_send_dispatch`, checks pass, cap full | queued (reopen) | turn `id#n` queued in the shared FIFO; `Retained` stays true |
| 4 | parked or queued (reopen) | slot granted | live, unconfirmed | relaunch with resume in the same cwd or worktree, template and environments; the message is the opening turn |
| 5 | live, unconfirmed | hook reports `session_id == SessionID` | live | clear `reopenUnconfirmed`; normal run from here on (row 2 applies again) |
| 6 | live unconfirmed, or queued (reopen) | session mismatch, pane exits, launch error, cancel, or daemon restart before row 5 | **parked** | turn rejected or interrupted with explicit text; mismatch or early exit sets `ResumeError`; never a fresh session, never released |
| 7 | parked | `leo_release` | released (`manual`) | worktree cleanup with safeguards |
| 8 | parked | `now - EndedAt ≥ 7d` | released (`retention`) | same as row 7 |
| 9 | live, confirmed | idle close, pane gone, caller gone, cancel, timeout, restart settle | closed, failed, canceled or timeout | as today: `Retained=false`, worktree cleanup, close notice (header only if the result was delivered) |
| 10 | live or closed | `leo_release` | released | unchanged |
| 11 | parked | daemon restart | parked | not touched by reconcile or prune |

## Steps

The steps run in the order listed. Step 7 doesn't depend on Steps 3–6 and can run alongside them. Every test command is `env -u TMUX go test -race -run '<names>' <pkgs>`; never run `make e2e` from a dispatch pane.

### Step 0: live probe (no code, no daemon)
- Run in a scratch tmux session with a `--settings` hook that dumps each payload to a file:
  - (a) `claude --resume <id> "hi"`: is the SessionStart and UserPromptSubmit `session_id` the same id? Is `source` set to `resume`?
  - (b) Resume with the transcript deleted: what's the exit code and stderr?
  - (c) Resume under a different `CLAUDE_CONFIG_DIR`: fresh session, or an error?
  - (d) codex 0.167.1: does `codex resume <id>` work as a TUI, and do codex hook payloads carry `session_id`?
- Record the results in the PR body.
- If (a) gives a new id, stop and tell the orchestrator, because the guard in Step 9 needs a different confirmation signal.
- If (d) fails, codex is not resumable (D2).

### Step 1: record fields and predicates
- Starting test: `TestResumablePredicate`, a table test:
  - claude, parked, with a session id → true
  - no session id → false
  - `ResumeError` set → false
  - released → false
  - codex → according to D2
  - opencode → false
  - a pre-upgrade closed record (JSON without the new fields) → not parked
- Files: `record.go` (fields), a new `resumable.go` (predicates and `harnessResumesInteractive`).
- Command: `env -u TMUX go test -race -run 'TestResumablePredicate' ./internal/consult/`

### Step 2: default on for every role
- Starting test: `TestResolveReleaseOnFinishDefaultsOnForEveryRole`. With nil input, `implement`, `""`, a template dispatch and `plan` all give true; an explicit `false` gives false.
- Change `ResolveReleaseOnFinish` (`release_on_finish.go:16`). Update existing tests that expected false for implement.
- Command: `… -run 'TestResolveReleaseOnFinish' ./internal/consult/`

### Step 3: closing a pane is no longer releasing the run
- Refactor: pull the pane kill out of `releaseLocked` into `retirePaneLocked(id, layout, commit func(*runState))`. Release becomes one way to commit it; `parkDeliveredLocked(id, turnID)` becomes the other and replaces `releaseDeliveredLocked`.
- `closableOnFinishLocked` replaces `releasableOnFinishLocked`. It requires `turnID` to be the last turn and implements row 2.
- Callers:
  - `collectDelivered` (`notify_inline.go:199`), which now passes `item.key`
  - `releaseReturned` (`release_on_finish.go:89`), renamed `closeReturned`
- Guard parked runs from cleanup in three places:
  - `cleanupPrecheck` (`worktree.go:184`): not due
  - `closeRecordedPaneThen` (`pane_cleanup.go:92`)
  - `collectRun` (`consult.go:1103`)
- Check what `onCollect` does, and that it's harmless to run it on a parked run that may reopen.
- Starting tests:
  - `TestDeliveredResultParksPaneKeepsWorktree`: the fake runtime's `Kill` gets the pane; status is closed with `Retained`; the worktree is `present` and its directory exists; there's no `#closed` key.
  - `TestWaitReturnedResultParksRun`
  - `TestSecondTurnDeliveryParksAgain`
  - `TestStaleTurnDeliveryDoesNotPark`
  - `TestTruncatedResultKeepsPane`
  - `TestTypedTurnKeepsPane`
  - `TestReleaseOnFinishFalseKeepsPane`
  - `TestCollectRunSkipsParkedWorktree`
- Adapt `release_on_finish_test.go` to the new behaviour.
- Command: `… -run 'Park|KeepsPane|ReleaseOnFinish|CollectRun' ./internal/consult/`

### Step 4: `leo_release` means "done"
- Starting tests:
  - `TestReleaseParkedRemovesCleanWorktree`
  - `TestReleaseParkedKeepsDirtyWorktree`: kept, directory intact
  - `TestReleaseParkedEvictedRecord`: no in-memory state; uses the `recorder.Resume` path (`release.go:157`)
  - `TestSendAfterReleaseRejected`: the message names the reason
- Change: `releaseLocked` sets `ReleaseReason` and clears `Retained`. `releasedSendError` covers `manual`, `retention` and the legacy on-finish case.
- Command: `… -run 'ReleaseParked|SendAfterRelease' ./internal/consult/`

### Step 5: keep retained records out of every cleanup path
- Starting tests:
  - `TestRecorderPruneKeepsParkedRecords`: more than 21 records, and a parked run without a worktree survives (`fsrecorder.go:144-215`)
  - `TestRestartReconcileKeepsParkedWorktree` (`restart_reconcile.go:300-349`)
  - `TestPruneWorktreesSkipsParked` (`prune.go:~188`): reports "retained for follow-up until <EndedAt+TTL>"
  - `TestMarkInterruptedLeavesParkedRun` (`consult.go:1309`)
- Evicting a parked run from memory (`pruneTerminalRunsLocked`, `consult.go:792`) stays allowed, because reopen rebuilds from disk.
- Command: `… -run 'Parked' ./internal/consult/ ./internal/cli/`

### Step 6: retention TTL sweep
- `var resumableRetention = 7 * 24 * time.Hour` (D1).
- `Dispatcher.SweepRetention(now)`, called from the housekeeping loop in `internal/web/consult_runtime.go:236-250`:
  - runs at most every 10 minutes, plus once after the restart reconcile;
  - skips `RestartPendingIDs`;
  - takes `serialLocks` for each id;
  - skips any run whose in-memory state is reopening or releasing;
  - calls `releaseLocked` with reason `retention`.
- Uncommitted work is protected by the existing `assessWorktree` checks: dirty, unmerged or unverifiable is kept; a live writer is deferred.
- Starting tests (fake clock):
  - `TestRetentionReleasesExpiredParkedRun`
  - `TestRetentionKeepsDirtyWorktreeOnExpiry`: released, `WorktreeKept`, files intact
  - `TestRetentionSkipsFreshParkedRun`
  - `TestRetentionSkipsRestartPending`
  - `TestRetentionSkipsReopeningRun`
- Command: `… -run 'Retention' ./internal/consult/ ./internal/web/`

### Step 7: resume argv for interactive launches
- `LaunchRequest` gains `ResumeSessionID` and `Reopen bool`.
- `TmuxInteractiveRuntime.Launch` sets `spec.Session{SessionResume, id}`.
- Codex (only if D2 passes): add an optional `harness.InteractiveResumer` interface, implemented with codex's existing `refreshSessionArgs`.
- The bridge's fallback relaunch, used when the mod never connects, must keep the resume args.
- Starting tests (assert the real argv string, as the "mocked exec seams" lesson says):
  - `TestLaunchWithResumeSessionPassesResumeArgv`: the tmux command contains `--resume` followed by the id, plus the positional read from the brief file, and no `--session-id`
  - `TestBridgeFallbackRelaunchKeepsResume`
  - `TestCodexInteractiveResumeArgs` in `./internal/harness/codex/`
- Command: `… -run 'Resume' ./internal/consult/ ./internal/harness/claude/ ./internal/harness/codex/`

### Step 8: reopen on follow-up (new file `reopen.go`)
- `SendWithConfig` (`continuation.go:19`) sends parked interactive runs to `d.reopen(ctx, cfg, rec, msg)`.
- **Checks that reject without changing anything** (no slot taken):
  - `resumable(rec)` must hold; otherwise the error is "dispatch X closed and is not resumable: <reason>; start a new dispatch".
  - The template must still exist and its harness must be unchanged.
  - Claude: `TranscriptPath` must exist and sit under the config dir the reopen environment resolves to (`session.ExplicitConfigDir` or `session.ConfigDir`, `internal/session/slug.go:28-65`).
  - `resumeWorkspace(rec)` must succeed (`continuation.go:280`).
- **Admission**:
  - Nested runs go through `admitNested` under `nestMu`.
  - Take a slot with `slotsFor(s).AcquireOrEnqueue()`.
  - If the run was evicted, rebuild its state from `recorder.Resume` and `newRunState`.
  - Append turn `id#n`, zero `EndedAt`, set status to queued or running, set `reopenUnconfirmed`, and call `startActive`.
  - When queued, set `awaitingSlot` and reuse `launchWhenSlotFree` (`interactive.go:218`). Otherwise call `launchInteractive` directly.
- **Launch request**:
  - The caller's recorded `CallerPaneID`, `CallerSessionID` and `CallerWindowID` go through the normal placement logic (D5).
  - The prompt is the raw message, not wrapped in the dispatch preamble (`requestPrompt` is bypassed when `Reopen` is set; D8).
- **Returns**: `SendResult{TurnID, Delivered, Queued}`.
- Starting tests:
  - `TestSendToParkedRunReopensWithResume`: the LaunchRequest has the same cwd or worktree, template, environments and `ResumeSessionID`; the turn is `#2`; its result is delivered exactly once, and the run parks again.
  - `TestReopenQueuesUnderCap`: FIFO order with a new dispatch that queued earlier; no Launch until a slot frees.
  - `TestReopenRejectsNotResumable`: covers no session, `ResumeError`, and the harness; `InUse` is unchanged.
  - `TestReopenRejectsMissingTranscript`
  - `TestReopenRejectsConfigDirMismatch`
  - `TestReopenAfterEviction`
  - `TestReopenWorktreeDeletedExternally`
  - `TestSecondSendWhileReopenQueuedRejected`
  - `TestReopenNestedRespectsDescendantCap`
  - `TestReopenUsageNotDoubleCounted`: the resumed transcript includes earlier turns (check `usage_live.go`)
- Command: `… -run 'Reopen' ./internal/consult/`

### Step 9: never start fresh, and failed reopens go back to parked
- In `Report` (`interactive.go:901`): while `reopenUnconfirmed` is set, a payload `session_id` equal to `SessionID` confirms the resume. A different id runs `failReopenLocked`, which:
  - kills the pane;
  - rejects the turn with the text "could not resume session X (harness started Y); nothing was continued";
  - sets `ResumeError`;
  - returns the run to parked;
  - never overwrites `SessionID`.
- These also return the run to parked before confirmation: the pane dies (`Sweep`, `finishInteractiveLocked`), the launch errors (`interactive.go:315`), cancel while queued, and `MarkInterrupted` with a queued reopen.
- After confirmation, everything behaves as today (row 9).
- Starting tests:
  - `TestReopenFreshSessionDetectedRevertsToParked`: no close notice, worktree retained
  - `TestReopenPaneExitBeforeConfirmRevertsToParked`
  - `TestReopenLaunchErrorRevertsToParked`
  - `TestCancelQueuedReopenRevertsToParked`
  - `TestRestartWithQueuedReopenRevertsToParked`
  - `TestConfirmedReopenIdleCloseIsTerminal`
- Command: `… -run 'Reopen|Revert' ./internal/consult/`

### Step 10: notifications
- Closing on delivery sends nothing (Step 3 already asserts this).
- A close notice is header only when the last turn's result reached the caller: either `ResultCollectedAt` is set or the turn's notification is `delivered`, truncated or not. "Header only" means one line with no inline block, and the pointer becomes `full output: leo_dispatch_output <id>` instead of `collect with leo_wait`.
- When the result never reached the caller, the notice keeps today's inline body.
- Change `deliveryMessage` (`notify_inline.go:42`) and `closeNoticeHeader` (`notify_close.go:47`).
- Starting tests:
  - `TestIdleCloseNoticeOmitsDeliveredResult`: a run opted out of closing, clock advanced 1h
  - `TestIdleCloseNoticeAfterTruncatedDeliveryIsHeaderOnly`
  - `TestIdleCloseNoticeCarriesUndeliveredResult`
- Command: `… -run 'CloseNotice' ./internal/consult/`

### Step 11: roster and API projection
- Add `Resumable bool json:"resumable,omitempty"` to `observe.Dispatch` (`internal/observe/snapshot.go:302`). Set it from `resumable(rec)` in `observedDispatch` (`observe_dispatch.go:223-257`).
- Confirm parked runs aren't filtered out the way released ones are.
- The tmux status-bar roster keeps its current glyph (D9).
- Starting test: `TestObservedDispatchMarksParkedResumable`, covering parked → true, `ResumeError` → false, released → false.
- Command: `… -run 'Observed' ./internal/consult/ ./internal/observe/`

### Step 12: tool descriptions and docs (docs only, so no failing test)
- `tools.go`:
  - `:505` (`leo_dispatch`): read-only and every other role close on delivery; a follow-up reopens.
  - `:595-646` (`leo_send_dispatch`): reopen, queueing, "not resumable" errors.
  - `:760` (`leo_release`): done with this run; drops the worktree under safeguards; later follow-ups rejected.
  - `:779` (`releaseOnFinishSchemaDescription`).
- Update any `internal/mcp` tests that assert these strings.
- `docs/configuration/dispatches.md`:
  - `:21`, `:454-466`, `:479`
  - rewrite `:485-551` as "Close on delivery and reopen", including the state table and the 7-day retention
  - `:594` (restarts), `:633` (contrast with headless continuation), `:679` (records)
- `docs/specs/2026-07-31-observability-api.md`: add `resumable` to the Dispatch shape.
- Flag for the orchestrator (outside this repo): `ai-config` `instructions/claude-coding.md`. It feeds the "Pane cleanup" paragraph of `~/.leo/agents/CLAUDE.md`, which tells orchestrators to `leo_release` implement runs by hand.

### Step 13: merge gate
- Check the load average first. Above about 30, run targeted packages only and note what was deferred.
- Run `heavy env -u TMUX go test -race ./internal/consult/ ./internal/mcp/ ./internal/web/ ./internal/observe/ ./internal/harness/... ./internal/session/ ./internal/cli/` in the background, then `make lint`.
- e2e is run by the orchestrator from a shell that isn't a dispatch pane, and only with `env -i … TMUX_TMPDIR=$(mktemp -d) make e2e`.
- Live acceptance runs on an **isolated test daemon** with its own `LEO_HOME` and port, never `leo service restart`:
  1. An implement dispatch with a worktree: after its notification the pane closes, and the record has no `#closed` key.
  2. A follow-up ("what was my first instruction?") reopens a pane in the same worktree that remembers the earlier conversation, and the result arrives once.
  3. `leo_release` removes the worktree, and a further follow-up is rejected.
  4. A result over the inline cap keeps its pane until `leo_wait` collects it.

## Open decisions

- **D1, retention TTL.** No existing TTL can be reused: `deferredQuietAfter=24h` only checks that writers have gone quiet, `RecordsKept=20` is a count, `leo dispatch prune` is manual, and `Dispatcher.Prune()` has no production caller. **(rec)** 7 days in a package var; add a config knob later if anyone asks.
- **D2, codex.** **(rec)** Support it through the `InteractiveResumer` capability only if Step 0(d) shows codex resumes as a TUI and its hooks report `session_id`. Otherwise codex returns "not resumable". Opencode doesn't apply.
- **D3, which closes can reopen.** **(rec)** Only the close on delivery, as the spec says. Idle, pane-gone, caller-gone and restart closes stay terminal. Broadening it is a possible follow-up.
- **D4, typed turns.** The old rule was "exactly one turn". **(rec)** Close after any delivered orchestrator turn that is the last turn, unless the run has any typed turn at all, since someone is using the pane.
- **D5, where the reopened pane goes.** **(rec)** Use the recorded caller fields with the normal placement logic, falling back to the background session if the caller is gone.
- **D6, failed reopens.** **(rec)** Checks that fail before anything is admitted change nothing. A failure after admission but before confirmation returns the run to parked, setting `ResumeError` when the failure is permanent. Never auto-release; release or the TTL cleans up.
- **D7, cancel.** **(rec)** Cancelling a queued or unconfirmed reopen returns the run to parked. Cancelling after confirmation works as today.
- **D8, follow-up wording.** **(rec)** Send the raw follow-up text without the dispatch preamble.
- **D9, status bar.** **(rec)** Only `/api/v1` gets the new `resumable` field; the tmux status bar keeps ✓.
- **D10, field name.** **(rec)** Keep the `release_on_finish` name and JSON field, and document the new meaning.

**Accepted risk:** a reopened pane adopted after a daemon restart, before its resume was confirmed, is treated as confirmed. This needs a restart to land inside the window between launch and SessionStart.

## Orchestrator decisions (2026-11-14)
D1–D10 accepted as recommended. Step 0 gates the never-fresh guard: if `claude --resume <id> "msg"` does not keep the session id, stop and report before Step 7.

## Addendum (planner, late): e2e
Step 2 defaults release_on_finish on, so e2e/interactive_dispatch_test.go `TestInteractiveDispatchLifecycle` (:87) and `TestInteractiveReleasePane` (:24) should pass `release_on_finish: false` explicitly. Add one e2e case for close → reopen → release. The Step 13 gate runs e2e with the `env -i` recipe, never from a dispatch pane.
