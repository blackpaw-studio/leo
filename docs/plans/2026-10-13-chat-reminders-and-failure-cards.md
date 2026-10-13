# Plan: Chat reminders and failure cards

Spec: `docs/specs/2026-10-13-chat-reminders-and-failure-cards.md` (wins on any conflict). Branch `feat/chat-failure-cards`.
Two PRs: **Part 1** (failure cards) merges first; **Part 2** (reminders) branches from it as `feat/chat-reminders`.

Rules for every step: write the failing test first and watch it fail for the stated reason. Use fake clocks and the fixture's `manualTimers`. No `time.Sleep`. Event waits may use a 5 s guard deadline, which fails the test and is not a sleep. One commit per step (`feat:`/`test:`/`docs:`). Times are stored as UnixMilli, like `messages.ts`.

## Part 1 — Failure cards (PR 1)

**Design.** The chat service formats the card and posts it through the existing `Post` path. That reuses `ResolveOwner`, the missing-owner system note, `kind=task` and the push publisher. The daemon socket serves `POST /chat/task-failure` by delegating to the web server's socket-only `ChatHandler()` mux. The TCP mux never registers it. The runner calls the route through a package-level seam (same pattern as `persistent.go`'s `enqueueFollowUp`) and logs a warning on failure.

### 1.1 Chat service: `TaskFailure`
- Files: new `internal/chat/failure.go`, `internal/chat/failure_test.go`.
- Interface: `type TaskFailureRequest struct{ Task, RunID string; Attempts int; Error, LogPath string }` and `func (s *Service) TaskFailure(ctx context.Context, req TaskFailureRequest) (Message, error)`.
- Failing tests (package `chat`, using `newFixture`):
  - `TestTaskFailurePostsACardToTheOwner`: task `build` with workspace `/src/web` gives agent `web`, `Kind==KindTask`, `Task`/`RunID` set, `DirectionOut`, and the exact text below.
  - `TestTaskFailureWithMissingOwnerFallsBackWithANote`: `Owner:"gone"` puts the card in `assistant`'s thread after a system note naming `gone`.
  - `TestTaskFailureErrorSummary`, a table:
    - a 600-rune multibyte error keeps exactly the first 500 runes, with no ellipsis;
    - an error containing a triple backtick still gives one code block;
    - an empty `LogPath` omits the `Log:` line (see open question 1).
  - `TestTaskFailureRejectsBadRequests`: an unknown task, an empty task, an empty `run_id` or `attempts < 1` each return `ErrInvalid`.
- Minimal impl:
  - Validate the request.
  - Build the text:
    `**Task failed: <task>**\nRun \`<run_id>\` · <n> attempt(s)\n<fence>\n<first 500 runes>\n<fence>\nLog: \`<log_path>\``
    The fence is three backticks, or one longer than the longest backtick run in the summary.
  - Return `s.Post(ctx, PostRequest{Caller: taskCallerPrefix + task, Task, RunID, Text})`.
- Verify: `go test -race -run TestTaskFailure ./internal/chat/`

### 1.2 Web: socket-only handler
- Files: `internal/web/handlers_chat.go`, `internal/web/handlers_chat_test.go`.
- Interface: `ChatHandler()` also registers `POST /chat/task-failure` → `handleChatTaskFailure`. Do **not** add it to `registerChatRoutes`.
  - Body: `{task, run_id, attempts, error, log_path}`, decoded with `decodeChatJSON`.
  - Reply: `200 {id, agent}`.
  - `chatAvailable` returns 503; errors go through `writeChatError`.
- Failing test `TestChatTaskFailureRouteIsSocketOnly`:
  - Write the test server's `leo.yaml` with a `build` task, following the `auth_agent_token_test.go:23` pattern.
  - Through `cs.s.ChatHandler()`: 200, and the card is in `assistant`'s thread.
  - Through `chatRequest` (the TCP handler) with the operator token and with the agent token: 404 both times.
- Verify: `go test -race -run 'TestChatTaskFailure|TestChatSocketHandler' ./internal/web/`

### 1.3 Daemon: socket route
- Files: `internal/daemon/chat.go` (add const `chatTaskFailurePath = "/chat/task-failure"`), `internal/daemon/server.go` (next to line 287: `mux.HandleFunc("POST "+chatTaskFailurePath, s.handleChat)`), `internal/daemon/chat_test.go`.
- Failing test `TestSocketTaskFailureRouteDelegatesToTheWebServer`:
  - Before `setChat`: 503. It currently returns 404 from the mux, which is the failure to watch for.
  - After `setChat`: reaches the stub handler (Teapot) with path `/chat/task-failure`.
- Verify: `go test -race -run TestSocket ./internal/daemon/`

### 1.4 Daemon client: `ReportTaskFailure`
- Files: new `internal/daemon/chat_client.go` and `chat_client_test.go`.
- Interface:
  - `type TaskFailureRequest struct{Task, RunID string; Attempts int; Error, LogPath string}` with JSON tags `task, run_id, attempts, error, log_path`.
  - `ReportTaskFailure(ctx, workDir string, req) error`: unix client, bounded by `taskFailureReportTimeout = 10*time.Second`.
  - `ReportTaskFailureHTTP(ctx, baseURL, req) error`, the test twin.
  - Both share `reportTaskFailure(ctx, cli, baseURL, req)`, mirroring `publishTaskRun`.
- Failing tests:
  - `TestReportTaskFailurePostsTheCard`: an httptest server asserts `POST /chat/task-failure` and the JSON body; on 200 it returns nil.
  - `TestReportTaskFailureSurfacesTheDaemonError`: a 503 `{"ok":false,"error":"Leo Chat is unavailable…"}` returns an error containing that text, decoded via `Response`.
- Verify: `go test -race -run TestReportTaskFailure ./internal/daemon/`

### 1.5 Runner: post the card on failure
- Files: `internal/run/runner.go`, `internal/run/runner_test.go`.
- Interface:
  - `var reportTaskFailure = func(ctx context.Context, homePath string, req daemon.TaskFailureRequest) error { return daemon.ReportTaskFailure(ctx, homePath, req) }`
  - `func postFailureCard(cfg, taskName, runID string, attempts int, err error, logFile string)`. It sets `LogPath = filepath.Join(cfg.StatePath(), "logs", logFile)` when `logFile != ""`. On error it prints `warning: posting the failure card to Leo Chat: …` to stderr.
- Impl (replaces `runner.go:374`):
  `if lastErr != nil && !interrupted && task.NotifyOnFail { postFailureCard(…, runID, maxAttempts, lastErr, logFile); if len(task.Channels) > 0 { notifyFailure(…) } }`
- Test helper: `stubFailureCards(t) *[]daemon.TaskFailureRequest`, which swaps the var and restores it with `t.Cleanup`. Apply it to the two existing `TestRunNotifyOnFail*` tests too, so they stay hermetic.
- Failing test `TestRunPostsFailureCard`, a table. `execCommand` makes the main attempt run `false` and the notify child `true`.
  - notify + channels: 1 card, and the notify child also ran.
  - notify, no channels: 1 card and exactly 1 exec.
  - `notify_on_fail: false`: 0 cards.
  - The reporter returns an error: `Run` still returns the claude error.
  - Assert the payload:
    - `Task` is `mytask`;
    - `RunID` has prefix `mytask-`;
    - `Attempts == Retries+1` (use `Retries: 1`);
    - `Error` contains `exit status 1`;
    - `LogPath` is under `<HomePath>/state/logs`, and the file exists.
- Also extend `TestRunInterruptStopsImmediatelyWithoutRetryOrNotify` to assert 0 cards.
- Verify: `go test -race -run 'TestRunPostsFailureCard|TestRunNotifyOnFail|TestRunInterruptStops' ./internal/run/`

### 1.6 Docs
- `docs/configuration/chat.md`:
  - New `## Task failure cards` section: trigger, owner, text, coexistence with channels, persistent and interrupted runs excluded.
  - In `## API`, a paragraph on the socket-only `POST /chat/task-failure {task, run_id, attempts, error, log_path}`, used by `leo run` and not served on TCP.
- `notify_on_fail` no longer requires `channels` for oneshot tasks. Fix the wording in:
  - `docs/configuration/config-reference.md:267,269`
  - `internal/templates/skills/config-reference.md:43-44`
  - `docs/cli/run.md:55`
  - the `internal/cli/task.go:317` flag help
  - the `internal/config/config.go:349` comment

### 1.7 Part 1 final checks
- `make test` and `make lint`. No argv or config-schema change, so e2e is not required. If any step ends up touching argv or config, run only `env -i HOME=$HOME PATH=$PATH TMUX_TMPDIR=$(mktemp -d) make e2e`.
- Live check on an isolated test daemon: separate `LEO_HOME` and port. Never `leo service restart`; stop by killing the test PID.
  - Add a oneshot task with `notify_on_fail: true`, no channels, and `env: {ANTHROPIC_BASE_URL: http://127.0.0.1:1}` so it fails.
  - `LEO_HOME=… leo run <task>`.
  - Confirm the card via the socket: `GET /api/v1/chat/assistant/messages`.
  - Repeat with the daemon stopped: the warning prints, and the run's exit is unchanged.
- Review: dispatch `review` on `git diff main...HEAD`.

## Part 2 — Reminders (PR 2)

**Design.**
- Rows live in a `schedules` table.
- One loop goroutine (`runSchedules`) does this repeatedly: fire every due row, arm `opts.After` for the earliest pending row, then wait on that timer, a `rearm` kick, or `s.ctx.Done()`.
- `Schedule` and `CancelSchedule` kick the loop. A stale timer is harmless: firing is decided by an SQL guard (`fired_at IS NULL AND cancelled_at IS NULL`), not by which timer woke the loop.
- `Recover()` fires past-due rows synchronously, then starts the loop (counted in `s.active`, so `Close` waits for it).
- Firing runs `UPDATE schedules SET fired_at … WHERE pending` and the message `INSERT` in one transaction, which rules out a double post.

### 2.1 Migration step 3 + store
- Files: `internal/chat/migrate.go`, new `internal/chat/schedules_store.go`, `model.go` (`type Schedule struct{ID, Agent, Kind, Task, RunID, Text string; FireAt, Created time.Time}`), `store_test.go`, `migrate_test.go`.
- Step 3 SQL:
  `CREATE TABLE schedules (id TEXT PRIMARY KEY, agent TEXT NOT NULL, kind TEXT NOT NULL, task TEXT NOT NULL DEFAULT '', run_id TEXT NOT NULL DEFAULT '', text TEXT NOT NULL, fire_at INTEGER NOT NULL, created_at INTEGER NOT NULL, fired_at INTEGER, cancelled_at INTEGER); CREATE INDEX schedules_pending ON schedules(fire_at) WHERE fired_at IS NULL AND cancelled_at IS NULL;`
- Store methods:
  - `InsertSchedule`
  - `PendingSchedules(agent)`, ordered by `fire_at, id`
  - `NextPending() (Schedule, bool, error)`
  - `DuePending(now)`
  - `CancelSchedule(id, agent string, at time.Time) error`: `ErrNotFound` when the id is unknown or belongs to another agent; `ErrConflict` when the row has already fired or been cancelled.
  - `FireSchedule(id string, m Message, firedAt time.Time) (bool, error)`: one transaction. If the guarded UPDATE touches no rows, return false and insert nothing. Extract an `insertMessageTx(tx, m)` from `InsertMessageWith` for this.
- Failing tests:
  - `TestOpenStoreUpgradesAV2DatabaseAddsSchedules`: `migrate(db, migrations[:2])`, then `OpenStore` gives `user_version` 3 and the table is usable.
  - `TestStoreSchedulesListOnlyPending`
  - `TestStoreFireScheduleIsOnceAndAtomic`: a second fire, or firing a cancelled row, returns false and adds no message.
  - `TestStoreCancelScheduleErrors`
- Verify: `go test -race -run 'TestOpenStore|TestStoreSchedule|TestStoreFireSchedule|TestStoreCancelSchedule' ./internal/chat/`

### 2.2 Fixture prerequisites (test-only, committed with 2.3)
- `testClock` gets a mutex around `Now`/`Advance`, because the loop reads `Now` concurrently and `-race` would flag it.
- `recordingPublisher` gets a buffered channel and a `waitMessage(t) Message` helper for `EventMessage`.
- Add `f.restart(t)`: closes `f.svc` and builds a new `Service` on the same store and options.

### 2.3 `Service.Schedule` + validation
- Files: new `internal/chat/schedule.go` and `schedule_test.go`.
- Interface: `type ScheduleRequest struct{Caller, Task, RunID, Text, At string}` and `func (s *Service) Schedule(ctx, req) (Schedule, error)`.
- Impl:
  - `enter`/`leave`.
  - Text: trimmed, non-empty, at most `MaxTextBytes`.
  - `time.Parse(time.RFC3339, At)`, which requires an offset or `Z`.
  - Reject `!at.After(now)` with "at must be in the future". Reject `at.After(now.AddDate(1,0,0))` with "at must be within one year".
  - `withRun` then `thread`; post no note at schedule time.
  - Kind is `task` when `Task != ""`, else `text`.
  - `IDs.New()`, `InsertSchedule`, `kick()`.
- Failing tests:
  - `TestScheduleValidation`, a table:
    - rejected with `ErrInvalid`: `tomorrow`, no offset, exactly now, past, now+1y+1s, empty text, oversize text, unknown caller;
    - accepted: now+1y.
  - `TestScheduleFromATaskRunTargetsTheOwner`: owner `web`, kind `task`, and the task and run ids set.
- Verify: `go test -race -run TestSchedule ./internal/chat/`

### 2.4 List + cancel
- Interface: `Schedules(ctx, ScheduleListRequest{Caller, Task, RunID}) ([]Schedule, error)` and `CancelSchedule(ctx, ScheduleCancelRequest{Caller, Task, RunID, ID}) error`. Both resolve the thread with `withRun` + `thread`. A malformed id returns `ErrNotFound`. A successful cancel kicks the loop.
- Failing tests:
  - `TestReminderListIsScopedToTheCallersThread`: soonest first; a `task:build` run owned by `web` sees `web`'s reminders.
  - `TestReminderCancelErrors`: unknown or another thread's id → `ErrNotFound`; fired or cancelled → `ErrConflict`; ok → the reminder leaves the list.
- Verify: `go test -race -run 'TestReminderList|TestReminderCancel' ./internal/chat/`

### 2.5 Timer loop + firing
- Files: `schedule.go` and `service.go`:
  - `rearm chan struct{}` with capacity 1, created in `NewService`;
  - `kick()`, a non-blocking send;
  - `runSchedules`, `fireDue`;
  - `Recover` change;
  - refactor `insert` into `insertWith(m, write func(Message) error)` so a firing takes its id and ts under `insertMu`.
- Late fires: `lateFireThreshold = 60*time.Second`. When `now.Sub(FireAt) > threshold`, append `"\n\n_(late — scheduled for " + FireAt.Local().Format("Mon 2 Jan 2006 15:04 MST") + ")_"`. Publish `EventMessage` only when `FireSchedule` returns true. Log errors through `Logf`.
- Failing tests (all call `f.svc.Recover()` first):
  - `TestReminderFiresAtItsTime`: arm `== 2h` via `<-f.timers.got`; `Advance(2h)`; `fireLast`; `waitMessage` gives `assistant`, kind `text`, exact text, and nothing left pending.
  - `TestReminderRearmsForTheEarliest`: arms read 2h, then 1h, then 2h again after cancelling the 1h reminder.
  - `TestCancelledReminderNeverFires`: cancel, advance, fire the stale timer, schedule +3h and read its arm as the sync point; there is no reminder message.
  - `TestReminderFromATaskRunFiresAsATaskCard`
  - `TestRestartPostsAPastReminderOnceWithTheLateFlag`, a table with lateness 30 s → no flag, 60 s → no flag, 61 s → flag. Flow: `restart` + `Recover` gives exactly one message; a second `restart` + `Recover` still gives one.
- Verify: `go test -race -run 'TestReminder|TestCancelledReminder|TestRestartPostsAPastReminder' ./internal/chat/`

### 2.6 Agent tool routes
- Files: `internal/web/web.go` (apiMux, next to lines 645–647), new `internal/web/handlers_chat_schedule.go`, `handlers_chat.go` (add `At string json:"at"` and `ID string json:"id"` to `chatToolRequest`), `handlers_chat_test.go`.
- Routes:
  - `POST /api/agent/{name}/chat/schedule` → `{id, at}`, with `at` in RFC3339 and the daemon's local offset;
  - `…/chat/schedule/list` → `[{id, at, text}]`, `[]` when empty;
  - `…/chat/schedule/cancel` → `{id, cancelled: true}`.
  All go through `decodeChatTool` (which refuses dispatch subagents with 403) and `writeChatError`.
- Failing test `TestChatScheduleToolRoutes`: `dispatch_id` → 403; schedule, then list, then cancel succeed; a second cancel → 409; a past `at` → 400.
- Verify: `go test -race -run TestChatScheduleToolRoutes ./internal/web/`

### 2.7 MCP tools
- Files: `internal/mcp/chat.go`, `internal/mcp/chat_test.go`, `internal/leotools/leotools.go` (add the three names after `leo_chat_history`).
- `registerChat` adds `leo_chat_schedule` (`text`, `at` required), `leo_chat_schedule_list` (no args) and `leo_chat_schedule_cancel` (`id` required). Each refuses a set `dispatchID`. The MCP side only checks that arguments are present and are strings; the daemon checks the time. `chatToolRequest` gains `At` and `ID`. Verbs are `schedule`, `schedule/list` and `schedule/cancel`, and requests carry `r.task`/`r.runID`.
- Failing tests:
  - `TestChatScheduleToolsCallTheDaemon`: paths, bodies and verbatim results, including a task-run registry.
  - Missing `text`, `at` or `id` → error, with no daemon call.
  - Extend `TestChatToolsRefuseDispatchSubagents` to all six tools.
  - `TestRegistryNamesMatchLeotools` already guards the name list.
- Verify: `go test -race -run 'TestChat|TestRegistryNamesMatchLeotools' ./internal/mcp/`

### 2.8 Docs + final checks
- `docs/configuration/chat.md`:
  - Agent tools: "three MCP tools" becomes six, with three new table rows.
  - New `### Reminders`: validation, thread ownership, how it fires (text vs task card), the late flag, survival across restarts, and that recurring and editing are out of scope.
- `make test` and `make lint`. No argv or config change, so no e2e. If any step does touch argv or config, run only the `env -i …` e2e form from 1.7.
- Live check on the isolated test daemon:
  - With the isolated agent token, POST `…/chat/schedule` with `at` = now+90 s and confirm it posts.
  - Schedule another, kill the test daemon past its time, start it again, and confirm one post carrying the late line.
- Review: dispatch `review.concurrency` (timer loop, at-most-once firing) on `git diff feat/chat-failure-cards...HEAD`.

---

## Risks / open questions

1. **Failed log write.** When the runner can't write the log, `logFile` is empty. The spec always shows `Log:`; the plan leaves the line out in that case. Confirm, or render `Log: (not written)`.
2. **Small formats the spec leaves open.**
   - **Late-line timestamp layout:** `Mon 2 Jan 2006 15:04 MST`.
   - **Late-line separator:** `\n\n`, so it renders as its own Markdown paragraph.
   - **`at` in replies:** RFC3339 in the daemon's local offset, since the originally submitted offset isn't stored.
   - **Missing-owner note:** a reminder from a task whose owner is missing posts no system note at schedule time.
3. **Error text is terse.** The card's error is `lastErr`, often just `exit status 1`, because the spec names `lastErr` rather than the harness output. The log path is what makes the card useful.
4. **The new tests still use a global seam.** `reportTaskFailure` is a package var swapped in tests, not true dependency injection. That matches `enqueueFollowUp`/`execCommand`, but it is only safe because `internal/run` tests don't use `t.Parallel`.
5. **Long timers.** `time.After` timers up to a year out are dropped on each re-arm. Go 1.25 (per `go.mod`) garbage-collects unreferenced timers, so there's no leak.
6. **Fixture changes in 2.2.** These touch shared chat test helpers (`testClock` mutex, publisher channel). Existing tests should be unaffected; the full `./internal/chat/` run in 2.5 confirms it.
