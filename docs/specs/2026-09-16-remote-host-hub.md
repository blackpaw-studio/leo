# Remote host hub: the local daemon owns remote connections

Requested by leoterm (Ghostty fork with a native Agents sidebar) so that any
client (leoterm, web UI, CLI) only ever talks to the local unix socket.
Supersedes the foreground `leo host forward` command.

## Goal

The local daemon connects to every configured remote leo host, keeps the
connection alive, proxies agent routes under `/hosts/{name}/...`, and
re-emits remote observability events over the unix socket with a `host`
field. Clients never manage SSH processes.

## Non-goals

- Per-host observability URLs or web-UI pages.
- Changing how remote daemons work beyond running the same leo version.
- Authentication on the unix socket. Socket file permissions remain the
  boundary, as today.
- Answering SSH prompts. The daemon fails fast and tells the client what to
  run interactively.

## Design

### Package `internal/hosts`

`Hub` owns one `connection` per entry in `client.hosts`, plus the implicit
`localhost`. The connection state machine:

```
disconnected -> connecting -> connected -> disconnected (drop, backoff)
                          \-> error (fatal: auth / host key / config)
```

- `connect` resolves the remote socket (existing `printf` probe over ssh),
  starts `ssh -N -T -L <state>/remotes/<name>.sock:<remote sock>` with
  `ControlMaster=auto`, `ControlPath=<state>/remotes/<name>.ctl`,
  `BatchMode=yes`, keepalives, unlink-on-bind. Moved verbatim from
  `internal/cli/host_forward.go`, which is deleted.
- Health: poll the forwarded `/health` until 200, then every 15 s; a failed
  poll or ssh exit moves to `disconnected` and schedules reconnect with the
  existing 1-30 s exponential backoff. Backoff resets after 60 s healthy.
- `error` is terminal until the next explicit `connect`. Classification
  from ssh stderr: `Permission denied` -> `ssh_auth_required`,
  `Host key verification failed` -> `ssh_host_key_unknown`,
  `Connection refused|timed out|No route` -> `ssh_unreachable`, else
  `ssh_failed`. The last 10 stderr lines go in `error`.
- Lazy: the first proxied request to a `disconnected` host triggers
  `connect` and waits up to 20 s for `connected`; otherwise 503.
- `client.hosts.<name>.autoconnect: true` connects at daemon start.
- `Hub.Close()` on daemon shutdown: `ssh -O exit` per host, remove both
  sockets.

All state transitions publish `host_state_changed` on the `observe.Bus`.

### Daemon routes (unix socket, `daemon.Response` envelope)

| Route | Effect |
|---|---|
| `GET /hosts` | `[{name, local, default, ssh?, state, error?, code?, connected_at?}]`; `localhost` first with `state:"local"`. |
| `POST /hosts/{name}/connect` | Idempotent. Starts connect if needed, returns the row immediately (state may be `connecting`). |
| `POST /hosts/{name}/disconnect` | Stops the forward, returns the row. |
| `GET /templates` | Local template list, same shape as `leo template list --json`. |
| `/hosts/{name}/agents/...`, `/hosts/{name}/templates` | Reverse proxy (`httputil.ReverseProxy` over a unix-socket transport) with the prefix stripped. Status and body pass through unchanged. `localhost` dispatches to the local mux in-process. Not connected -> 503 `{code:"host_unavailable", error}`. |
| `GET /events` | SSE. `hello {version, ...}` first, then one `host_state_changed` per known host (including `localhost`, state `local`) so a fresh subscriber learns current states without racing `GET /hosts`, then `: ping` every 20 s and the same event names/payloads as `/api/v1/events` with `host` added to every payload. `host_state_changed {host, state, error?, code?}` also fires on every transition. |
| `GET /state` | `{agents: [...]}`, same row shape as `/api/v1/state.data.agents` with `host` per row, merged across `localhost` and every `connected` host. |
| `GET /health` | `{ok:true, data:{version}}`. |
| `GET /version` | `{ok:true, data:{version}}`. |

Every route above except the SSE stream returns the standard
`daemon.Response` envelope: `{ok:true, data:<payload>}` on success,
`{ok:false, error, code}` on failure. So `GET /hosts` is
`{ok:true, data:[...]}`, `GET /state` is `{ok:true, data:{agents:[...]}}`,
`GET /templates` is `{ok:true, data:[...]}`. Proxied `/hosts/{name}/...`
responses are the remote's envelope passed through unchanged.
Unknown host name -> 404 `{ok:false, code:"host_unknown", error}`.

### Event fan-in

The SSE and state handlers in `internal/web/handlers_observe.go` move to a
shared `internal/observe/httpapi` package used by both the web mux (auth
unchanged) and the unix mux. For each host that reaches `connected`, the
hub opens `GET /events` on the forwarded socket, tags every event with
`host`, and republishes on the local bus. The subscription ends on
disconnect and restarts on reconnect. A remote that lacks `/events`
(older version) proxies agent routes normally, emits only
`host_state_changed`, and its `/state` rows come from `/agents/list` on
demand.

### CLI

`leo host forward` is removed. `leo host list`, `leo host connect <name>`,
`leo host disconnect <name>` call the daemon. `leo agent --host <name>`
keeps SSHing the remote binary per call (unchanged) but passes the hub's
`ControlPath` so it reuses the master connection; attach therefore does
not re-authenticate while the hub is connected.

### Config

`client.hosts.<name>.autoconnect: bool` (default false). No other change.

### Version

Ships in v0.29.0. Remotes must run >= v0.29.0 to emit events through the
hub; older remotes are proxied without events.

## Testing

- `internal/hosts`: state-machine table tests with a fake ssh seam
  (exact argv asserted, including `BatchMode=yes` and `ControlPath`),
  stderr classification table, backoff reset, lazy-connect timeout,
  `Close` teardown order.
- Daemon: proxy tests against an in-process fake remote on a unix socket
  (status passthrough, 503 on disconnected, 404 on unknown host, localhost
  in-process path); SSE test asserting `hello`, tagged events, ping cadence
  with a fake clock; `/state` merge across two hosts.
- CLI: `host list|connect|disconnect` argv/HTTP tests; `agent attach --host`
  argv includes the hub ControlPath.
- e2e (`make e2e`): a second isolated leo daemon on this machine registered
  as host `loop` over `ssh localhost` with BatchMode; assert connect,
  proxied `agents/list`, an `agent_spawned` event carrying `host:"loop"`,
  disconnect, and reconnect after killing the ssh process.

## Rollout

Single PR on `feat/remote-host-hub`, after the dispatch settings-menu PR
merges. leoterm re-derives its contract from this document.
