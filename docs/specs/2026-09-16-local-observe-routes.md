# Local observability routes

Leo exposes five unauthenticated, read-only routes on its local Unix socket:

- `GET /health` returns `{"ok":true,"data":{"version":"...","ready":false,"pid":1234}}`. It answers 200 as soon as the socket binds; `ready` flips true only once boot has finished restoring agents, and `pid` identifies the daemon process so a caller that restarted it can tell the new daemon from the old one. `leo update` and `leo service restart` wait on both (`daemon.WaitReady`).
- `GET /version` returns `{"ok":true,"data":{"version":"..."}}`.
- `GET /events` streams the same local SSE events as the web API: a `hello` frame first, named event frames, and a `: ping` heartbeat every 20 seconds.
- `GET /state` returns `{"ok":true,"data":{"meta":{"seq":N},"agents":[...],"dispatches":[...]}}`. Agent rows match `/api/v1/state.data.agents`; `dispatches` (live plus lingering, `[]` when none, never omitted) and `meta.seq` match `/api/v1/state`. `meta.seq` is read before any state, so the snapshot is never older than its seq and a client applies only `/events` frames with a greater `state_seq`.
- `GET /templates` returns the same sorted rows as `leo template list --json`, wrapped in the daemon response envelope.

## Baselining from `/state` + `/events`

The bus has no replay, so a client must order the two calls like this (the same rule as `/api/v1/state`; see `2026-10-07-dispatch-attach.md` section 4):

1. Subscribe to `GET /events` first and read `hello.seq`. Buffer the frames that arrive from here on.
2. Then `GET /state`.
3. Drop buffered events with `seq <= data.meta.seq` (the snapshot already reflects them) and apply the rest.
4. Refetch `/state` under the condition `2026-10-07-dispatch-attach.md` section 4 gives (events may have fallen between the two calls), then repeat step 3.

Event payloads carry whole entities, so re-applying one is harmless. A buffered event with `seq > meta.seq` can still briefly replay an older row state (for example `running` after the snapshot already shows `done`); the next `dispatch_changed` for that dispatch corrects it. `meta.seq` is `0` when the daemon has no event source.

These routes describe only the daemon on the local socket. They do not merge remote hosts, accept a scope parameter, or add host fields to payloads.
