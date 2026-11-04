# Agent Progress

An agent can advertise long-running work (a build, a deploy, a test sweep) as **tasks** with progress. Leo's apps show them next to the agent and notify when one finishes. The daemon owns the data; the apps only render it. Tasks live in memory: a daemon restart clears them.

## Reporting

From inside the agent's own session, either:

- the `leo_progress` MCP tool: `{id, title?, percent?, indeterminate?, status?, eta_seconds?, state?}`
- the CLI, for scripts in the agent's pane:

```bash
leo progress build --title "Xcode build" --percent 10 --eta 5m
leo progress build --percent 62 --status "Compiling 412/980"
leo progress deploy --title "Deploy" --indeterminate
leo progress build --done      # or --fail / --cancel
```

Leo tells each agent about `leo_progress` itself: the MCP server's `initialize` carries a short instruction (Claude Code adds it to the system prompt), and Leo appends the same line to a Codex agent's developer instructions. Nothing needs adding to your own config.

Both authenticate with the agent's per-launch attention token (`LEO_ATTENTION_TOKEN`, with `LEO_WEB_PORT` and `LEO_API_TOKEN`), so an agent can only write its own tasks: there is no agent argument, and the name is never taken from the request or `LEO_PROCESS_NAME`. Only agents Leo launched with hooks have a token; other agents, and `leo_dispatch` subagents, get a readable error. `leo progress` exits 2 outside an agent.

## Automatic tasks

Leo also creates tasks on its own, so progress shows up without the agent reporting anything. They carry `"source": "auto"` (agent-reported tasks are `"source": "agent"`) and are always indeterminate: the apps show elapsed time, never an ETA.

- **Long shell commands.** A command an agent runs (the `Bash` tool) becomes a task once it has run **20 seconds**; a shorter one never shows. The title is the tool's `description` when it has one, otherwise the program and its first argument (`heavy xcodebuild`): never the whole command line, which can hold secrets. It ends `succeeded`, `failed` (a failing exit) or `cancelled` (interrupted, or still open when the turn ended). A background shell stays running until its task-notification, `TaskStop` or the end of its `Stop` list.
- **Dispatches.** Every running `leo_dispatch` run is a task on the agent that started it (nested runs roll up to the root). The title is `<name or template> · <role>` and the status is `queued`, `running`, `waiting` or `waiting on you`. Done or idle ends it `succeeded`, failed or timeout `failed`, canceled, closed or released while running `cancelled`. A follow-up turn starts a fresh run.

Auto tasks clear themselves 60 seconds after they end, whatever their end state (commands fail routinely, so a failure never persists), send no finish notification, are never stale and cannot be dismissed (`409`). They have their own cap of 8 running per agent, counted apart from agent-reported tasks; past it new ones are skipped. When an agent reports its own running task with `leo_progress`, its running automatic command tasks are removed: the agent's task is the richer view of the same work. Dispatch tasks are never removed that way.

The ids `auto.…` are reserved for the daemon: an agent report whose id starts with `auto.` is rejected (`400`).

Commands are tracked from the hooks Leo already relays for a supervised agent, plus an async `PreToolUse` hook limited to `Bash`. **Hooks are set when an agent launches, so an agent that was already running picks the `PreToolUse` hook up at its next relaunch**; Leo does not restart it. Until then, commands the agent runs in the background still appear (they are armed by the `PostToolUse` that starts them), and dispatch tasks work immediately. opencode has no hooks, so it gets dispatch tasks only.

Codex: its shell tool names are matched against a fixed list (`shell`, `local_shell`, `exec_command`, `container.exec`) that has not been verified against a live Codex session, and Codex reports no failure exit. A Codex command therefore only succeeds or is cancelled, and a Codex tool with another name is not tracked.

## Task fields

| field | rules |
|---|---|
| `id` | 1–64 characters of `[a-z0-9._-]`; the agent's own key |
| `title` | required on the first report, at most 80 characters |
| `percent` | 0–100 (clamped); implies a determinate bar. `indeterminate: true` is the alternative, not both |
| `status` | at most 120 characters, e.g. `Compiling 412/980` |
| `eta_seconds` | seconds from now, stored as an absolute `eta_at` |
| `state` | `running` (default), `succeeded`, `failed` or `cancelled` |

Reports are upserts: omitted fields keep their value. Strings are trimmed and stripped of control characters. A report to an ended task starts a fresh run that keeps only the title.

## Lifecycle

- A running task with no update for 15 minutes is flagged `stale`; any update clears it.
- `succeeded` and `cancelled` tasks are removed 10 minutes after they end.
- `failed` tasks stay until dismissed.
- Auto tasks are removed 60 seconds after they end (see above).
- All of an agent's tasks are removed when it stops (including restart and reset).
- Bounds per agent: 8 running tasks (a 9th is rejected with `409`), 8 failed and 8 finished tasks (the oldest is dropped). Auto tasks have the same bounds, counted separately: 8 running (a 9th is skipped, silently) and 8 ended.

## API

- `POST /api/agent/progress` (agent bearer): `{"token", "id", ...}` → `200 {"ok": true, "task": {...}}`. Errors are readable: `400` validation (including a reserved `auto.` id), `403` unknown or expired token, `409` too many running tasks.
- `DELETE /api/v1/agents/{name}/tasks/{id}` (operator token) and `DELETE /agents/{name}/tasks/{id}` (socket) dismiss an ended (succeeded, failed or cancelled) task: `204`, `404` unknown, `409` if the task is still running or is an auto task. Agents cannot dismiss.
- `GET /api/v1/state` agent rows carry `tasks` (omitted when empty); the `/events` stream carries `agent_task_changed` with the whole task, or `task: null` when it is gone. Publishes are coalesced to at most two per second per task, and a terminal state or removal goes out at once.
- `hello.features` advertises `agent_progress`.
