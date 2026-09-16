# Local observability routes

Leo exposes four unauthenticated, read-only routes on its local Unix socket:

- `GET /health` returns `{"ok":true,"data":{"version":"..."}}`. `GET /version` is an alias with the same response.
- `GET /events` streams the same local SSE events as the web API: a `hello` frame first, named event frames, and a `: ping` heartbeat every 20 seconds.
- `GET /state` returns `{"ok":true,"data":{"agents":[...]}}`; agent rows match `/api/v1/state.data.agents`.
- `GET /templates` returns the same sorted rows as `leo template list --json`, wrapped in the daemon response envelope.

These routes describe only the daemon on the local socket. They do not merge remote hosts, accept a scope parameter, or add host fields to payloads.
