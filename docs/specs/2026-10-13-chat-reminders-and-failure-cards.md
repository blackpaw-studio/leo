# Chat reminders and failure cards

Two independent Leo Chat daemon gaps from the Telegram cutover plan
(`~/.leo/workspace/leo-chat-migration-plan.md` §5). Each ships as its own PR.
No client (iOS/Mac) wire change: both reuse existing message kinds.

## Part 1 — Failure cards (cutover blocker)

**Behavior.** When a `runtime: oneshot` task run ends in failure (all attempts
exhausted, not interrupted) and `notify_on_fail: true`, the daemon posts a card
into the task owner's thread, whether or not the task has `channels`.

- Owner: `chat.ResolveOwner` (same as task-run posts), including its
  `DefaultAgent` + system-note fallback.
- Message: `direction=out`, `kind=task`, `task=<name>`, `run_id=<id>`. Pushes
  through the existing notifier unchanged.
- Text (Markdown):
  ```
  **Task failed: <name>**
  Run `<run_id>` · <attempts> attempt(s)
  <error summary, first 500 chars of lastErr, single code block>
  Log: `<log path>`
  ```
  The `Log:` line is omitted when the runner could not write a log.
- Transport: the runner is a separate `leo run` process, so it calls a new
  daemon route over the existing socket client
  (`POST /chat/task-failure {task, run_id, attempts, error, log_path}`). The
  route is socket-only (CLI IPC), not exposed on the TCP/web listener. Daemon
  unreachable or chat disabled → log a warning; the run's own result is
  unaffected.
- Covers both `leo run` and cron firings (both go through `run.RunWith`).

**Channels.** When `channels` are also set, the existing channel notification
still fires too. Rationale: a task only has both during the transition; at
cutover `channels` are removed, so the duplicate disappears without a code
change, and Telegram stays a safety net until then.

**Out of scope.** Persistent tasks (`runtime: persistent`): they run inside a
supervised agent and never reach `notifyFailure`; the agent is already in its
own thread. Interrupted runs post nothing (unchanged).

## Part 2 — Reminders (`leo_chat_schedule`)

**Tools** (MCP, same caller rules as `leo_chat_post`: supervised agents and
task runs; dispatch subagents get the standard refusal):

| Tool | Args | Returns |
|------|------|---------|
| `leo_chat_schedule` | `text` (Markdown, same limit as post), `at` (RFC3339 with offset) | `{id, at}` |
| `leo_chat_schedule_list` | — | caller's pending reminders `[{id, at, text}]`, soonest first |
| `leo_chat_schedule_cancel` | `id` | `{id, cancelled: true}`; error if unknown, not the caller's, or already fired |

- Validation: `at` must parse as RFC3339 and be strictly after now; reject
  past/now with a clear error. Reject `at` more than 1 year out.
- Ownership: a reminder belongs to the thread it will post into (the caller's
  agent thread; for a task run, the task owner's thread via `ResolveOwner`,
  resolved at schedule time). List/cancel see only reminders in the caller's
  thread.
- Firing: posts `direction=out`, `kind=text` into that thread — a normal
  message, so it pushes. For a task-run caller, `kind=task` with
  `task`/`run_id` of the scheduling run, matching how task posts render.

**Persistence.** New migration step adds a `schedules` table
(`id, agent, kind, task, run_id, text, fire_at, created_at, fired_at, cancelled_at`).
A single in-daemon timer (injected `Now`/`After` from `chat.Options`) arms for
the earliest pending row; schedule/cancel re-arm it. `Service.Recover()` loads
pending rows at startup.

**Late fires.** A row whose `fire_at` is more than 60 s in the past when it
fires (daemon was down) still posts, with a trailing line
`_(late — scheduled for <fire_at in daemon local time>)_`. Firing marks
`fired_at` in the same transaction as the message insert, so a crash cannot
double-post.

**Out of scope.** Recurring reminders, editing a reminder, markdown rendering
in the apps.

## Testing

- Part 1: runner test asserting the daemon failure call (payload incl. log
  path) fires with and without channels, and not for interrupted runs or
  `notify_on_fail: false`; daemon/service test for the card's owner, kind and
  text, including owner fallback.
- Part 2: service tests with a fake clock — fires at `at`, rejects past/far
  times, cancel prevents the fire, list scoped to caller, restart with a past
  row posts once with the late flag, dispatch caller refused at the MCP layer.
- Docs: `docs/configuration/chat.md` gains the three tools, the
  failure-card behavior, and the new socket route.
