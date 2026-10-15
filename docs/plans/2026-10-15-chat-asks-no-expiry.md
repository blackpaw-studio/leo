# Plan: Chat asks that don't expire

Spec: `docs/specs/2026-10-15-chat-asks-no-expiry.md` (wins on any conflict). Branch `feat/chat-asks-no-expiry`.

Rules for every step:
- Write the failing test first and watch it fail for the stated reason.
- Use the chat fixture (`newFixture`, `testClock`, `manualTimers`, `fakeDeliverer`, `recordingPublisher`). No `time.Sleep`. A 5 s guard on a channel receive is allowed: it fails the test and is not a wait.
- One commit per step (`feat:`/`test:`/`docs:`).
- Times are stored as UnixMilli.
- `go test` always runs with `-race`.

## Design decisions (settled here; the spec left them open)

1. **Schema v4.** Three columns are added to `messages`:
   - `deadline INTEGER NOT NULL DEFAULT 0` (unix ms; 0 means no deadline)
   - `closed_reason TEXT NOT NULL DEFAULT ''`
   - `asker TEXT NOT NULL DEFAULT ''`

   The same step runs `UPDATE messages SET state='expired', closed_reason='timeout' WHERE kind='ask' AND state='open'`. Pre-v4 open asks only exist after a crash, and the old Recover would have expired them anyway.

2. **Message fields.**
   - `Deadline time.Time` → `json:"deadline,omitzero"` (Go 1.25).
   - `ClosedReason string` → `json:"closed_reason,omitempty"`.
   - `Asker string` → `json:"-"`. It holds the caller as given: an agent name, or `task:<name>`.
   - Constants: `ClosedTimeout = "timeout"`, `ClosedWithdrawn = "withdrawn"`, `ClosedAgentGone = "agent_gone"`.
   - `AskResult` gains `ClosedReason string` → `json:"closed_reason,omitempty"`. Any settled-without-answer ask keeps `timed_out: true`.

3. **Waiter registry invariant.** An open ask has exactly one `*waiter` in `s.waiters` while this daemon runs:
   - `insertOpen` adds it for new asks; `Recover` adds it for asks restored from the store.
   - `settle` removes it. Only the winner of the store compare-and-swap (`Transition`/`CloseAsk` returning `ok`) calls `settle`. `expireAsk` today settles even when it loses; fix that.
   - `waiter` gains `blocked int`, guarded by `waiters.mu`:
     - `join(wt) bool`: counts the call as blocked; returns false if `wt` is already settled.
     - `leave(wt) (settled bool)`: uncounts the call and reports whether `wt` was settled while it was counted.
     - `settle(id, v) (blocked int)`: sets `v`, closes `done`, deletes the waiter from the map, and returns how many calls were blocked. It returns 0 when there was no waiter.

4. **"No call blocked" is race-free.** All three operations run under `waiters.mu`, after the store CAS.
   - **Answer vs. a blocked call.** `Answer` does CAS open→answered, then `n := settle(...)`, then delivers only if `n == 0`. A counted call always wakes on `done`. After the `select`, `waitAsk` calls `leave`. If it was settled while counted:
     - when `ctx.Err() == nil`, the call returns `askOutcome` (the answer);
     - otherwise the caller is gone, so the call hands the answer to late delivery itself.

     Result: one call → exactly one consumer; two counted calls → at most a duplicate, which the spec accepts.
   - **Answer vs. a concurrent resume.** The resume looks up the waiter, then `join`s it.
     - `join` before `settle`: the resume is counted, so there is no delivery and the resume returns the answer.
     - `settle` before `join`: `n == 0`, so the answer is delivered. The resume's `join` fails, or it finds no waiter and reads the store → answer. That is a duplicate, which is acceptable.
   - Answers to permission prompts never deliver (`Kind != KindAsk`).

5. **Late-delivery target.** Deliver only when `ask.Asker == ask.Agent`. That covers an agent's own asks and persistent-task turns whose owner is the running agent. Oneshot task runs (`task:T`) and persistent tasks with a foreign owner get no delivery; resume still returns the stored answer.

6. **Late-delivery message.** Insert an inbound message:

   ```
   Message{Agent: ask.Agent, Direction: in, Kind: text, ReplyTo: ask.ID, State: pending, Text: answerText(ask)}
   ```

   Publish it and enqueue it on the agent's serial queue through the existing `s.deliver`. That gives auto-start of a suspended agent with the "Starting…" note, capture, delivery ref, recovery of pending deliveries, and a failure note.

   `answerText` builds the text from:
   - the labels of the chosen values, joined with `", "`;
   - then `"\n" + text` if there is free text;
   - or `"(no selection)"` if there is neither.

   `FramePrompt`'s `replyLine` gains a `KindAsk` branch. It applies to any reply to an ask, including a user's manual reply. The prompt looks like this:

   ```
   [Leo Chat] Yes
   In reply to your leo_chat_ask 01JABC… (2026-10-15T18:00:00Z): Ship it?
   Answer: {"message_id":"01JABC…","values":["y"],"labels":["Yes"],"text":""}
   ```

   - The question is clipped to `MaxReplyCardRunes`.
   - The `Answer:` line appears only when the ask is `answered`. It is the compact JSON of the tool's result fields.

7. **Agent gone.** `agent.Manager` gets `SetGoneHook(func(name string))`. It is stored under a mutex because it is set after the IPC server is already serving.
   - `Stop` calls the hook after success when `!opts.WakeOnMessage` (an operator stop). The idle sweep passes `true`, so it never fires there.
   - `Delete` always calls it after removal.
   - Composition root (`internal/service/process.go`): after `srv.StartWeb(cfg, agentMgr)` returns nil, run `agentMgr.SetGoneHook(srv.CloseChatAsksOf)`.
   - `daemon.Server.CloseChatAsksOf` forwards to `web.Server.CloseChatAsksOf`, which forwards to `chat.Service.CloseAsksOf`. Each is nil-safe.
   - `agent` does not import `chat`, `web` or `daemon`, so there is no import cycle and no global. The mutex orders the `s.webServer` write before any hook read.

8. **Shutdown leaves asks open.**
   - `expireAsk` returns on `s.ctx.Done()` without expiring.
   - `waitAsk` selects on `s.ctx.Done()` and returns `pending`.
   - Chat closes before HTTP shutdown, so held calls still get their `pending` response.
   - Permission prompts keep expiring at shutdown and at startup (spec item 7). They get no `closed_reason`.

## Steps

### 1. Schema v4, model fields, store close operations
- Files: `internal/chat/migrate.go`, `model.go`, `store.go`, `migrate_test.go`, `store_test.go`, `model_test.go`.
- Changes:
  - Migration `{version: 4}` per decision 1.
  - `messageColumns`, `insertMessageTx` and `scanMessage` carry `deadline`, `closed_reason` and `asker`. A deadline of 0 scans to the zero `time.Time`.
  - `func (s *Store) CloseAsk(id, reason string) (bool, error)`: `UPDATE … SET state='expired', closed_reason=? WHERE id=? AND kind='ask' AND state='open'`.
  - `func (s *Store) CloseAsksOf(asker, reason string) ([]string, error)`: select and update in one transaction, like `moveAll`.
- Failing tests:
  - `TestOpenStoreUpgradesAV3DatabaseAddsAskColumns`: build a v3 database with `migrate(db, migrations[:3])` (like `TestOpenStoreUpgradesAV2DatabaseAddsSchedules`) holding an open ask and an answered ask. After `OpenStore`: `user_version` is 4, the open ask is `expired`/`timeout`, and the answered ask is unchanged.
  - `TestStoreRoundTripsAskDeadlineReasonAndAsker`.
  - `TestCloseAskClosesOnlyAnOpenAsk`: an open ask → true. A second call → false. An answered ask → false. An open permission prompt → false.
  - `TestCloseAsksOfClosesOnlyThatAskersOpenAsks`: another asker's ask and an answered ask are untouched.
  - `TestMessageJSONOmitsAnEmptyDeadlineAndReason`: both are absent when unset and present when set.
- Verify: `go test -race -run 'TestOpenStore|TestStore|TestCloseAsk|TestMessageJSON' ./internal/chat/`

### 2. Waiter accounting, deadline-free asks, shutdown keeps asks open
- Files: `internal/chat/outbound.go`, `prompts.go`, `service_test.go`, `internal/web/handlers_chat_test.go` (existing test update).
- Changes:
  - Delete `DefaultAskTimeout`. `validateAsk`: 0 means no deadline; `<0` or `>MaxAskTimeout` is still `ErrInvalid`.
  - `Ask` sets `m.Asker = req.Caller`. When `timeout > 0` it sets `m.Deadline = Now()+timeout` and starts `expireAsk`; otherwise there is no goroutine.
  - Add `closeAsk(id, reason) bool`: `Store.CloseAsk`; if ok, `settle(id, nil)` and `publishUpdated(id, ChangeState)`.
  - `expireAsk` uses `closeAsk(id, ClosedTimeout)`, returns on shutdown, and drops `defer waiters.remove`.
  - `waitAsk` follows decision 4. Call `join` **before** arming the window timer: tests sync on `timers.got`. For now the "caller gone and settled" branch returns pending; step 3 adds the hand-off.
  - `askOutcome`: `answered` → the answer; `open` → `Pending`; `expired` → `TimedOut` plus `ClosedReason`.
  - `Answer` and `settlePermissionInTerminal` use the new `settle` return value. In `expireAsk`, only the CAS winner settles.
- Failing tests:
  - `TestAskWithoutTimeoutNeverExpires`:
    - Ask with `Wait: 30m` and no `Timeout`. Exactly one timer is requested, and it is 30m (a non-blocking check that `timers.got` is empty afterwards).
    - Fire it → `Pending`; the message is `open` with a zero `Deadline`.
    - `clock.Advance(72h)`. A resume with `Wait: 30m` → fire → `Pending`, not `TimedOut`.
  - `TestAskWithTimeoutPersistsItsDeadline`: `Timeout: 10m` → stored `Deadline == TS+10m`. Fire the deadline → `expired`, `closed_reason: timeout`, and the call gets `TimedOut` with `ClosedReason: "timeout"`.
- Existing tests to update in this step:
  - `TestAskTimesOutAndExpires`: give it an explicit `Timeout: 5*time.Minute`.
  - `TestCloseCancelsAndDrainsWaitersAndDeliveries`: give the ask `Wait: 30m` so a timer exists. After `Close` the result is `Pending` and the message is still `open`.
  - `internal/web` `TestShutdownClosesChatFirst`: expect `"pending":true`, not `"timed_out":true`.
- Verify: `go test -race -run 'TestAsk|TestClose' ./internal/chat/ && go test -race -run TestShutdownClosesChatFirst ./internal/web/`

### 3. Late answer delivery and framing
- Files: `internal/chat/outbound.go`, `framing.go`, `framing_test.go`, `service_test.go`.
- Changes:
  - Add `func answerText(ask Message) string` and `func (s *Service) deliverAnswer(ask Message)` (decisions 5 and 6).
  - `Answer`: when `m.Kind == KindAsk && blocked == 0`, call `deliverAnswer` on the re-read message.
  - `waitAsk` hand-off branch: `deliverAnswer` on the re-read message when it is answered.
  - `replyLine` gets the `KindAsk` branch.
- Failing tests:
  - `TestFramePromptReplyToAnAnsweredAsk`: exact string, as in decision 6. Also a case for an `open` ask, which has no `Answer:` line.
  - `TestLateAnswerIsDeliveredOnce`:
    - Ask with `Wait: 30m` → fire → pending. `Answer("y")`, then `Flush`.
    - `delivered()` holds exactly one prompt with the exact framed text.
    - The thread holds an inbound `text` reply (`ReplyTo == ask.ID`) in state `delivered`.
    - A resume returns the answer. After another `Flush` there is still one delivery.
  - `TestAnswerWhileACallWaitsIsNotDelivered`: Ask with `Wait: 30m`, then `<-timers.got`, then `Answer`. The call returns the values. After `Flush`, `delivered()` is empty and there is no inbound message.
  - `TestAnswerAfterTheCallerLeftIsDeliveredOnce`: 25 iterations, each with a fresh fixture.
    - Ask with ctx and `Wait: 30m`, then `<-timers.got`.
    - `cancel()`, then `Answer`, in that order on the test goroutine.
    - Wait for `Ask` to return, then `Flush`.
    - Exactly one delivery and a `Pending` call result. This covers both the `leave`-first and `settle`-first interleavings.
  - `TestLateAnswerWakesASuspendedAgent`: with `deliver.stopped["assistant"] = true`, the "Starting assistant…" note appears and the answer is delivered.
  - `TestLateAnswerToATaskRunAskIsNotDelivered`: caller `task:build`, with owner `assistant` in the fixture config → no delivery. A resume returns the answer.
  - Extend `TestPermissionAnsweredFromChat`: no inbound message.
- Verify: `go test -race -run 'TestFramePrompt|TestLateAnswer|TestAnswer|TestPermission' ./internal/chat/`

### 4. Restart recovery
- Files: `internal/chat/service.go`, `store.go`, `service_test.go`, `store_test.go`.
- Changes:
  - `Store.ExpireOpen()` → `ExpireOpen(kind string)`; `moveAll` filters by kind. `Recover` expires only `KindPermission`.
  - New `recoverAsks`, run inside an `enter`/`leave` bracket so `Close` cannot race `active.Add`. For each open `KindAsk`:
    - no deadline → `waiters.add`;
    - deadline at or before `Now()` → `closeAsk(id, ClosedTimeout)`;
    - otherwise → `waiters.add`, then `go expireAsk` with `After(deadline - now)`.
- Failing tests (use `f.restart(t)` then `f.svc.Recover()`):
  - `TestRestartKeepsADeadlineFreeAskOpen`: the ask stays `open`. A later `Answer` → one delivery, because no call is blocked.
  - `TestRestartExpiresAnAskPastItsDeadline`: 10m ask, advance 11m → `expired`/`timeout`, and a `chat.updated` event is published.
  - `TestRestartRearmsTheRemainingDeadline`: 10m ask, advance 4m → `timers.got == 6m`. Fire → `expired`/`timeout`.
  - `TestResumeAfterRestartReturnsTheStoredAnswerOrPending`: answered before restart → the answer. Open → window fires → `Pending`, not `TimedOut`.
  - Rename `TestStartupExpiresOpenPrompts` → `TestStartupExpiresOpenPermissionPrompts`. It inserts a permission prompt and an ask; only the permission prompt expires.
- Verify: `go test -race -run 'TestRestart|TestResume|TestStartup' ./internal/chat/`

### 5. Withdraw (service)
- Files: `internal/chat/outbound.go`, `service_test.go`.
- Interface:
  - `type WithdrawRequest struct{ Caller, Task, RunID, MessageID string }`
  - `func (s *Service) WithdrawAsk(ctx context.Context, req WithdrawRequest) (Message, error)`
- Behavior:
  - `enter`/`leave`.
  - `validID`, else `ErrInvalid`.
  - Resolve the thread with `withRun` and `thread`, as `resumeAsk` does.
  - The message must be a `KindAsk` in that thread with `Asker == req.Caller`, else `ErrInvalid` ("not your ask").
  - Not `open`, or a lost `closeAsk` CAS → `ErrConflict`.
  - Otherwise `closeAsk(id, ClosedWithdrawn)` and return the stored message.
- Failing tests:
  - `TestWithdrawClosesTheCallersAsk`: the blocked call returns `TimedOut` with `ClosedReason: "withdrawn"`. The `chat.updated` payload's message has `closed_reason`. A later `Answer` → `ErrConflict`.
  - `TestWithdrawRefusesAnotherAgentsAsk`: caller `web` on `assistant`'s ask → `ErrInvalid`.
  - `TestWithdrawOfASettledAskConflicts`: an answered ask, and a second withdraw → `ErrConflict`. A malformed id → `ErrInvalid`.
- Verify: `go test -race -run TestWithdraw ./internal/chat/`

### 6. Close a gone agent's asks (service)
- Files: `internal/chat/outbound.go`, `service_test.go`.
- Interface: `func (s *Service) CloseAsksOf(agent string)`.
  - Does nothing once chat is closed.
  - Runs `Store.CloseAsksOf(agent, ClosedAgentGone)`; for each id, `settle(id, nil)` and `publishUpdated(id, ChangeState)`.
  - Logs errors.
- Failing test `TestCloseAsksOfClosesOnlyThatAgentsAsks`:
  - Set up a blocked `assistant` ask and an open `web` ask.
  - The `assistant` call returns `ClosedReason: "agent_gone"`; the `web` ask is still `open`.
  - Nothing is delivered and events are published.
- Verify: `go test -race -run TestCloseAsksOf ./internal/chat/`

### 7. `agent.Manager` gone hook
- Files: `internal/agent/manager.go`, `internal/agent/manager_test.go`.
- Changes:
  - Fields `goneMu sync.Mutex; gone func(string)`.
  - `SetGoneHook(fn)`, and `notifyGone(name)`, which copies the hook under the lock and calls it outside the lock.
  - Rename `Stop`'s body to `stop`. `Stop` calls `stop`, then `notifyGone` on success when `!opts.WakeOnMessage`. This covers every success return, including the record-less live agent.
  - `Delete` calls `notifyGone(name)` after `agentstore.Remove`.
- Failing tests (`capturingSupervisor` fixtures, as in `TestStopManualLeavesWakeOnMessageFalseSweepLeavesTrue`):
  - `TestOperatorStopReportsTheAgentGone`: one call, with the canonical name.
  - `TestIdleStopDoesNotReportTheAgentGone`: `WakeOnMessage: true`.
  - `TestFailedStopDoesNotReportTheAgentGone`: the `StopAgent` failure path.
  - `TestDeleteReportsTheAgentGone`: following the `attention_test.go` pattern.
- Verify: `go test -race -run 'TestStop|TestDelete|Gone' ./internal/agent/`

### 8. Wiring (glue: no new test; the live check in step 11 covers it)
- Files: `internal/web/handlers_chat.go` (`func (s *Server) CloseChatAsksOf(name string)`), `internal/daemon/server.go` (`func (s *Server) CloseChatAsksOf(name string)`), `internal/service/process.go` (`agentMgr.SetGoneHook(srv.CloseChatAsksOf)` right after a successful `StartWeb`).
- Fix the comments on `StartChat` and `Recover` ("expires prompts" → "recovers asks and prompts").
- Verify: `go build ./... && go test -race ./internal/service/ ./internal/daemon/`

### 9. HTTP route, MCP tool, tool registry
- Files: `internal/web/web.go`, `handlers_chat.go`, `handlers_chat_test.go`, `internal/mcp/chat.go`, `chat_test.go`, `internal/leotools/leotools.go`.
- Web:
  - Add `POST /api/agent/{name}/chat/ask/withdraw` with body `{message_id, task, run_id}` → `handleAgentChatAskWithdraw` → `{message_id, withdrawn: true}`. Errors go through `writeChatError`: 400 or 409.
  - Update the comment on `handleAgentChatAsk`.
- MCP `leo_chat_ask`:
  - `timeout_s` schema minimum 0, described as "omit or 0: no deadline".
  - `parseChatAsk`: no default. An integer 0..3600 is accepted; send `TimeoutS` only when it is greater than 0.
  - `WaitS = chatAskCallWindow`, or `min(timeout, window)` when there is a timeout.
  - Delete `defaultChatAskTimeout`.
  - Rewrite `chatAskDescription`: there is no deadline by default. A call waits at most 30 min and then returns `pending`. You don't need to keep resuming: a late answer arrives as a `[Leo Chat]` message replying to the ask. Withdraw an ask once it is moot.
- MCP `leo_chat_ask_withdraw`: `{message_id}` is required. It calls `chatCall(... "ask/withdraw", chatToolRequest{Task, RunID, MessageID})` and refuses dispatch subagents.
- Add `leo_chat_ask_withdraw` to `leotools.Names`.
- Failing tests:
  - `internal/mcp/chat_test.go`:
    - an ask without `timeout_s` → the body has no `timeout_s` and `wait_s == chatAskCallWindow`. Replace the `timeout_s == 300` assertion at line ~100.
    - `timeout_s: 0` is accepted; change the "timeout <= 0" rejection case to `-1`.
    - withdraw → the right path and body; withdraw without `message_id` → error.
    - the dispatch-refusal list includes withdraw.
    - `TestRegistryNamesMatchLeotools` fails until `Names` is updated.
  - `internal/web/handlers_chat_test.go`:
    - `TestAgentChatAskWithdrawRoute`: 200, then 409; another agent → 400.
    - `TestChatPayloadsCarryClosedReasonAndDeadline`: `GET /api/v1/chat/assistant/messages` shows `"closed_reason":"withdrawn"` and a `deadline` on a timed ask, and no `deadline` key on a deadline-free ask. A `chat.updated` event from `cs.events` carries `closed_reason`.
- Verify: `go test -race -run 'Chat|Registry' ./internal/mcp/ ./internal/web/ ./internal/leotools/`

### 10. Docs
- `docs/configuration/chat.md`:
  - The tools table becomes seven tools: `leo_chat_ask` `timeout_s` (optional, no default, max 3600) and a new `leo_chat_ask_withdraw` row.
  - Rewrite the ask lifecycle section: no deadline by default; the 30-minute call window and `pending`; late delivery with the exact example prompt; duplicates are possible; withdraw.
  - `leo agent stop` and delete close asks (`agent_gone`); idle-suspend, crash, graceful restart and daemon restart do not.
  - On restart, deadlines are re-armed, or the ask expires if its deadline has passed.
  - Message shape gains `deadline?` and `closed_reason?` (`timeout|withdrawn|agent_gone`). Add the withdraw agent route.
- `internal/templates/skills/agent-management.md`: asks stay open, so don't loop on `pending`. A late answer arrives as a chat message. Withdraw an ask once it is moot.
- Verify: `rg -n "300|default 300|timed_out" docs/configuration/chat.md internal/templates/skills/agent-management.md` shows nothing stale.

### 11. Final checks
- `make test`, then `make lint`. Run the two in separate calls and quote any failure in full.
- e2e: this plan touches neither argv nor config, so it is not required. If a fix touches either, run `env -i HOME=$HOME PATH=$PATH TMUX_TMPDIR=$(mktemp -d) make e2e`. Never run plain `make e2e` from a dispatch pane.
- Review: dispatch `review.concurrency` (waiter accounting and store CAS) on `git diff main...HEAD`. Bar: fix P0/P1 issues and anything a real user can hit. Record narrow races as accepted risks.
- Live check on an isolated test daemon (separate `LEO_HOME` and port). Never run `leo service restart`: it hits production. To restart, kill the test PID and start it again.
  1. Spawn a test agent and have it `leo_chat_ask` with no `timeout_s`. Interrupt the call.
  2. Restart the test daemon and confirm the ask is still `open` in `/api/v1`.
  3. Answer it with `POST /api/v1/chat/{agent}/messages/{id}/answer`. Confirm the agent pane receives the `[Leo Chat] … In reply to your leo_chat_ask <id>` prompt.
  4. `leo agent stop` the agent and confirm a second open ask becomes `expired`/`agent_gone`.
- For the orchestrator, outside the repo: send the iOS project, and the Mac app or leoterm, a note covering the optional `deadline`, `closed_reason`, deadline-free asks, and the late-answer inbound reply.

## Risks / accepted
- **Abandoned call without a cancel.** If a harness abandons a tool call without MCP `notifications/cancelled`, the call still counts as blocked. The answer is not late-delivered, but resume returns it. Leo's MCP server does propagate cancels (`internal/mcp/server.go:137`).
- **Late delivery cut short by shutdown.** The next Recover marks it `failed` with the "send it again" note. Resume still works.
- **Rename.** `asker` is not re-keyed, so a late delivery after a rename fails visibly.
- **Stop racing an answer.** An answer that lands between the process kill and `CloseAsksOf` can late-deliver and restart an operator-stopped agent. The window is narrow.
- **Oneshot task-run asks.** With no default deadline they stay open until answered or withdrawn. Nothing closes them when the run ends.

## Orchestrator amendments (override the plan above)

- **Asks without a delivery target keep a default deadline.** An ask whose
  `asker != agent` (oneshot `task:T` runs, persistent tasks with a foreign
  owner) has no one to hand a late answer to. When `timeout_s` is omitted,
  it gets the old `DefaultAskTimeout` (300 s) instead of no deadline. Fold
  this into step 2 with its own failing test. It closes the "oneshot
  task-run asks stay open forever" risk.
- **Web UI.** There is no chat view in `internal/web`, so the web-UI client
  note is out of scope here (see the spec amendment).
