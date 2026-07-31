# Observability API (`/api/v1`)

Read-only HTTP API exposing live agent and task state from the Leo daemon. Consumers:
The Den (pixel-office visualizer, separate repo), leoterm, the macOS app, and anything
else that wants to watch the fleet. See `2026-07-31-den-pixel-office.md` for the concept
that motivated it — but nothing in this API is Den-specific.

Two endpoints on the daemon's existing TCP (web) listener:

- `GET /api/v1/state` — full snapshot of the world right now.
- `GET /api/v1/events` — SSE stream of changes to that world.

The contract is snapshot-then-stream: a consumer fetches the snapshot, then applies
events. Every event carries enough state to be applied without a refetch.

## Design rules

- **Read-only.** No mutations here; control stays on the existing endpoints.
- **Consumer-agnostic.** No rooms, sprites, weather, or camera concepts. Those belong to
  the consumer.
- **Additive-only evolution.** Consumers must ignore unknown fields and unknown event
  types. New fields and event types are not breaking changes; removals and renames are,
  and require `/api/v2`.
- **Cheap.** The snapshot must be servable from in-memory state plus the agentstore; no
  shelling out to tmux per request.

## `GET /api/v1/state`

Wrapped in the existing `apiResponse` envelope (`{ok, data, error}`) used by every other
`/api` route:

```json
{
  "ok": true,
  "data": {
    "version": 1,
    "server_time": "2026-07-31T18:40:06-04:00",
    "leo_version": "0.10.3",
    "agents": [ /* Agent */ ],
    "tasks":  [ /* Task */ ],
    "recent_runs": [ /* TaskRun */ ]
  }
}
```

`GET /api/v1/events` is the one exception: SSE frames are written directly, with no
envelope.

### Agent

```json
{
  "name": "den",
  "template": "fable",
  "repo": "blackpaw-studio/leo-den",
  "workspace": "/Users/evan/.leo/agents/leo-den",
  "branch": "main",
  "status": "running",
  "activity": "working",
  "model": "fable",
  "harness": "claude",
  "restarts": 0,
  "started_at": "2026-07-31T18:42:33-04:00",
  "last_activity_at": "2026-07-31T18:44:01-04:00",
  "current_action": { "kind": "pane", "detail": "Running go test ./..." }
}
```

- `status` — lifecycle, from the agent record: `starting` | `running` | `suspended` |
  `stopped`.
- `activity` — live work state, from the activity tracker: `working` | `idle` |
  `unknown`. Orthogonal to `status`: a `running` agent may be `idle`. Non-running agents
  report `unknown`.
- `last_activity_at` — when the agent's tmux session last produced output or received
  input.
- `current_action` — best-effort, human-readable hint at what the agent is doing, or
  `null`. Only sampled for agents that are currently `working`.
  - `kind` describes where `detail` came from. **`pane` is the only kind Leo emits
    today**: the last non-empty line of the agent's tmux pane, with ANSI and control
    characters stripped, truncated to 120 characters. The field exists so a future
    structured source can be added without reshaping the object, so consumers must
    **render an unknown kind by falling back to displaying `detail` as plain text**
    rather than dropping the action.
  - `detail` is whatever the harness happens to be rendering: **untrusted display text**.
    Escape it before rendering, never parse it or branch on its contents, and handle it
    being absent or garbage.
- `model` / `harness` — resolved values after the defaults→template→agent cascade.

### Agent retention

`agents` lists **every agent Leo knows about, including stopped ones**. There is no
time-based aging out: an agent stays in the snapshot until it is explicitly removed from
the agent store (`leo agent reset`/remove), so a long-stopped agent keeps appearing
indefinitely with `status: "stopped"` and `activity: "unknown"`. Consumers that only care
about live agents must filter by `status` themselves.

Consequently, an agent disappearing from the snapshot means it was deleted, not that it
merely stopped — those are different situations and consumers should treat them
differently.

### How activity is derived

`tmux.ListSessionActivity` already reports `session_activity` (an epoch that advances on
pane output, injected input, and interactive typing) plus attached-client count for every
session in one `list-sessions` call. The tracker sweeps on a ticker (~2s):

- session_activity advanced since the previous sweep → `working`, and `last_activity_at`
  moves.
- no advance for longer than the idle threshold (15s) → `idle`.
- no tmux session, or agent not running → `unknown`.

One tmux call per sweep covers the whole fleet; a `capture-pane` is issued only for
agents that just advanced, so idle agents cost nothing. Deriving activity from tmux rather
than from Claude's session JSONL keeps this harness-agnostic — it works identically for
claude, codex, and opencode — and avoids coupling Leo to another tool's private on-disk
format.

### Task

Config-level description of a scheduled task: `name`, `schedule`, `timezone`, `enabled`,
`runtime` (`oneshot` | `persistent`), `template` (persistent only), `workspace`,
`model`, `harness`, `last_run_at`, `next_run_at`.

### TaskRun

A single firing: `id`, `task`, `status` (`running` | `succeeded` | `failed`),
`started_at`, `ended_at` (null while running), `duration_ms`, `error` (null unless
failed). `recent_runs` is capped (default 50, newest first).

A run also carries `workspace`, `model`, and `harness` — the values resolved for that
firing. They are deliberately denormalized rather than left as a join through `tasks[]`,
because the run producer already holds them and the join is not always available: a task
can be renamed, disabled, or deleted while one of its runs is still in flight, and a
`task_run_*` event can reach a consumer before it has ever fetched a snapshot. Carrying
them makes a run self-describing.

## `GET /api/v1/events`

`text/event-stream`. Each message is a named SSE event with a JSON payload:

```
event: agent_activity
data: {"seq":184,"at":"2026-07-31T18:44:01-04:00","agent":"den","activity":"working",
       "current_action":{"kind":"pane","detail":"Running go test ./..."}}
```

Every payload carries a monotonic `seq` and an `at` timestamp. On connect the server
sends a `hello` event with the current `seq`; a consumer whose stream drops should
reconnect and refetch the snapshot rather than trying to replay gaps (no event history is
retained). A comment heartbeat is sent every 20s so idle proxies don't close the stream.

Event types:

| Event | Payload beyond `seq`/`at` |
|---|---|
| `hello` | `version`, `server_time` |
| `agent_spawned` | full `Agent` object |
| `agent_state_changed` | `agent`, `status`, `restarts` |
| `agent_activity` | `agent`, `activity`, `current_action` |
| `agent_stopped` | `agent` |
| `task_run_started` | `TaskRun` |
| `task_run_succeeded` | `TaskRun` |
| `task_run_failed` | `TaskRun` (with `error`) |

`agent_suspended` / `agent_resumed` are represented as `agent_state_changed` with the
new `status`; consumers key off `status`, not distinct event names.

Slow consumers are dropped rather than buffered without bound: each subscriber gets a
bounded channel, and a subscriber that fills it is disconnected (it will reconnect and
resnapshot).

## Access

Both routes register on the existing `apiMux` and inherit its conventions unchanged:
bearer-token auth (`Authorization: Bearer <token>`) plus the global host/origin gating.
No new auth mechanism, no new middleware.

**Consumers are server-side, not browsers.** Leo adds no CORS headers and no exemption to
`hostOriginMiddleware`. A browser-direct consumer would force two bad outcomes — the
bearer token shipped into page source where any viewer can read it, and `EventSource`
cannot set an `Authorization` header at all. So a web consumer like The Den ships a thin
server that holds the token, consumes this API server-side, and re-exposes what its own
browser client needs on its own origin. That keeps the token server-side and leaves Leo's
security posture untouched.

## Testing

- Activity tracker: unit tests over the working/idle state machine and the pane-line
  sanitizer, driving the existing `activityExecCommand` seam with fixture output — no
  live tmux.
- Event bus: subscribe/publish fan-out, unsubscribe, and slow-consumer drop.
- Endpoints: `httptest` snapshot shape, SSE framing, heartbeat, and auth rejection.
