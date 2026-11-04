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

Both authenticate with the agent's per-launch attention token (`LEO_ATTENTION_TOKEN`, with `LEO_WEB_PORT` and `LEO_API_TOKEN`), so an agent can only write its own tasks: there is no agent argument, and the name is never taken from the request or `LEO_PROCESS_NAME`. Only agents Leo launched with hooks have a token; other agents, and `leo_dispatch` subagents, get a readable error. `leo progress` exits 2 outside an agent.

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
- All of an agent's tasks are removed when it stops (including restart and reset).
- Bounds per agent: 8 running tasks (a 9th is rejected with `409`), 8 failed and 8 finished tasks (the oldest is dropped).

## API

- `POST /api/agent/progress` (agent bearer): `{"token", "id", ...}` → `200 {"ok": true, "task": {...}}`. Errors are readable: `400` validation, `403` unknown or expired token, `409` too many running tasks.
- `DELETE /api/v1/agents/{name}/tasks/{id}` (operator token) and `DELETE /agents/{name}/tasks/{id}` (socket) dismiss an ended (succeeded, failed or cancelled) task: `204`, `404` unknown, `409` if the task is still running. Agents cannot dismiss.
- `GET /api/v1/state` agent rows carry `tasks` (omitted when empty); the `/events` stream carries `agent_task_changed` with the whole task, or `task: null` when it is gone. Publishes are coalesced to at most two per second per task, and a terminal state or removal goes out at once.
- `hello.features` advertises `agent_progress`.
