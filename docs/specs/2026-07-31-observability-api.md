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

```json
{
  "version": 1,
  "server_time": "2026-07-31T18:40:06-04:00",
  "leo_version": "0.10.3",
  "agents": [ /* Agent */ ],
  "tasks":  [ /* Task */ ],
  "recent_runs": [ /* TaskRun */ ]
}
```

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
  "current_action": { "kind": "tool", "tool": "Bash", "detail": "go test ./..." }
}
```

- `status` — lifecycle, from the agent record: `starting` | `running` | `suspended` |
  `stopped`.
- `activity` — live work state, from the activity tracker: `working` | `idle` |
  `unknown`. Orthogonal to `status`: a `running` agent may be `idle`. Non-running agents
  report `unknown`.
- `current_action` — best-effort description of what the agent is doing right now, or
  `null`. `kind` is `tool` | `thinking` | `unknown`. `detail` is truncated to 120 chars
  and must be treated as untrusted display text by consumers.
- `model` / `harness` — resolved values after the defaults→template→agent cascade.

### Task

Config-level description of a scheduled task: `name`, `schedule`, `timezone`, `enabled`,
`runtime` (`oneshot` | `persistent`), `template` (persistent only), `workspace`,
`model`, `harness`, `last_run_at`, `next_run_at`.

### TaskRun

A single firing: `id`, `task`, `status` (`running` | `succeeded` | `failed`),
`started_at`, `ended_at` (null while running), `duration_ms`, `error` (null unless
failed). `recent_runs` is capped (default 50, newest first).

## `GET /api/v1/events`

`text/event-stream`. Each message is a named SSE event with a JSON payload:

```
event: agent_activity
data: {"seq":184,"at":"2026-07-31T18:44:01-04:00","agent":"den","activity":"working",
       "current_action":{"kind":"tool","tool":"Bash","detail":"go test ./..."}}
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

Served on the web listener, subject to the same `web.allowed_hosts` gating as the UI —
same trust domain (LAN), no separate auth. Because consumers are browser apps served from
a different origin, both endpoints send permissive CORS headers for `GET` only. This is
acceptable precisely because the API is read-only; it must never be extended with
mutating routes under the same CORS policy.

## Testing

- Activity tracker: unit tests over the pane-hash state machine and the JSONL parser with
  fixture input, using the existing exec seams — no live tmux.
- Endpoints: `httptest` snapshot shape, SSE framing, heartbeat, slow-consumer drop, and
  host gating.
