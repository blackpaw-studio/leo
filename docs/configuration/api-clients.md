# API Clients

An **API client** is an agent Leo does not supervise — typically running in a
container, on another machine, or inside CI — that needs to message a Leo agent
or post into its chat thread. It authenticates with a bearer token of its own,
scoped to exactly that.

```yaml
api_clients:
  docker-scout:
    can_message: [rocket]      # start a turn in rocket
  pipe:
    can_post: ["pipe-*"]       # drop text into a chat thread, no turn
```

The two scopes are independent: `can_post` never grants `message`, and
`can_message` never grants `chat/post`. A client needs at least one.

This is not the same thing as the `client:` section, which configures *this*
machine's CLI as an SSH client of a remote leo daemon. Different direction,
different job.

## Why not just hand it the agent token

Leo already has a token for agents — `agent.token`, exported to every spawned
agent as `LEO_API_TOKEN`. It is unscoped: it works on every `/api/*` route and
every agent's message route. That is appropriate for an agent Leo started
itself, running as the same user, on the same machine.

A container is a different case. Its token sits in an image layer or a compose
file, somewhere Leo cannot see, and if it leaks it carries the whole fleet:
spawn, stop, run tasks, message anyone. An API client token is **default-deny**
— one route, the targets you name, nothing else.

## Creating one

```bash
leo client add docker-scout --can-message rocket
leo client add pipe --can-post 'pipe-*' --can-post wharf
```

`--can-message` and `--can-post` are repeatable and take agent names or globs.

This writes the `api_clients` entry, generates a token at
`~/.leo/state/clients/docker-scout.token` (mode 0600), and prints it once.

```bash
leo client list          # names, allowed targets, whether the token file exists
leo client rm docker-scout
```

`add` and `rm` reload a running daemon, so a new client works and a removed one
loses access (401) immediately, with no restart. If the daemon is not running,
the change applies when it next starts. If the reload fails, the command says
the change was saved but is **not active** (or **not revoked**); run
`leo service reload` once the daemon is healthy. Editing `leo.yaml` by hand
takes effect on `leo service reload`.

## What the token can do

Up to two things, one per scope:

```
POST /web/agent/<target>/message     # target matches can_message
POST /web/agent/<target>/chat/post   # target matches can_post
```

Everything else is refused with 403 —
every `/api/*` route, every other agent verb (`interrupt`, `send`, `stop`), the
whole browser UI, and `/login`. The check runs before the request reaches the
middleware the operator and agent tokens use, so there is no path by which a
client token falls through to them.

`can_message` and `can_post` entries match the literal path segment, exactly or as a glob
(`scout-*`). Unlike [template permissions](permissions.md), **an empty list
denies everything** and is rejected at config load. The inversion is
deliberate: an empty allowlist meaning "unrestricted" is a safe default for an
agent Leo spawned, and the wrong one for a token living outside Leo.

## Posting into a chat thread

`chat/post` stores a message in `<target>`'s [Leo Chat](chat.md) thread and
pushes it to paired devices, exactly as the agent's own `leo_chat_post` would.
It injects no turn and wakes no agent, so a pipeline can notify without costing
a model call.

```bash
curl -X POST http://leo:8370/web/agent/pipe-cam/chat/post \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"from": "pipe", "text": "cam offline"}'
# {"ok":true,"data":{"id":"01J…"}}
```

`from` follows the same rule as `/message`: the client's name, or
`<name>#<session>`. `text` is required and capped like `leo_chat_post`;
newlines and tabs are kept (chat renders Markdown) and other control
characters are stripped. Attachments are not supported.

| Status | Meaning |
|---|---|
| 200 | stored; `data.id` is the message id |
| 400 | bad `from`, empty or oversized text, invalid JSON |
| 401 | missing or unknown token |
| 403 | target outside `can_post`, wrong scope, any other path, method or `/api` route |
| 404 | target is in scope but is not a known agent |
| 503 | Leo Chat is not available on this daemon |

Scope is checked before the agent is looked up, so a client cannot probe which
agents exist outside its `can_post`. The stored message carries
`"sender": "client:<name>"` (never the `#session` suffix), and the push body is
prefixed `<name>: `.

## Sender identity is enforced, not asserted

Every request must carry a `from` that the client is entitled to claim:

```json
{"text": "build finished", "from": "docker-scout#ses_00274ca11ffe"}
```

`from` is either the client's own name or `<name>#<session>`, where the suffix
carries the caller's own session id so a reply can be addressed back to it
(bounded to 128 characters of `[A-Za-z0-9_.-]`). Anything else is a 400.

The daemon then **rewrites the message body** before delivery, stamping the
authenticated identity on it:

```
[message from docker-scout#ses_00274ca11ffe] build finished
```

This is not cosmetic. Leo types the message text verbatim into the target's
pane and never renders `from` there, so a client that controlled the text could
otherwise write its own `[message from ...]` line and impersonate another
sender to the receiving agent. The daemon strips an embedded prefix and
collapses newlines (which would submit the turn early) for the same reason.

Unlike template permissions — a guardrail enforced inside the agent's own
process — this is a boundary enforced by the daemon against a party it does not
trust.

## Reachability

Two settings decide whether a client can reach the daemon at all, and both fail
with **403 — the same status a scope denial returns**:

- `web.bind` defaults to `127.0.0.1`, so nothing outside the host can connect.
  A container needs an address it can route to.
- `web.allowed_hosts` must list the host or IP the client connects to, or the
  Host check rejects the request *before the token is looked at*. The `Host`
  header must also carry a port matching the listener — `Host: leo:8370` passes
  where a bare `Host: leo` does not, so a reverse proxy that rewrites or drops
  the port will be refused.

```yaml
web:
  enabled: true
  port: 8370
  bind: 0.0.0.0
  allowed_hosts: [host.docker.internal, 192.0.2.10]
```

`leo client add` prints a reminder when the current config would not be
reachable. If a client is getting 403 on a target you know is allowed, check
these before suspecting the token — the response body tells the two apart:

```
HTTP/1.1 403 Forbidden          HTTP/1.1 403 Forbidden
forbidden host                  {"ok":false,"error":"client \"docker-scout\" is not
                                 permitted to message \"infra\""}
   ^ Host/bind problem              ^ genuine scope denial
```

## Verified end to end

The boundary was exercised from a real Docker container against a live daemon
(`--add-host=host.docker.internal:host-gateway`, `web.bind: 0.0.0.0`,
`allowed_hosts: [host.docker.internal]`):

| Request from the container | Result |
|---|---|
| `POST /web/agent/rocket/message` (allowed) | reaches the handler |
| `POST /web/agent/infra/message` | 403 |
| `POST /web/agent/rocket/interrupt` | 403 |
| `POST /api/agent/spawn`, `GET /api/agent/list`, `GET /api/v1/state` | 403 |
| `GET /` | 403 |
| `from: rocket#ses_x` (forged) / `from` absent | 400 |

## Two-way messaging

Leo delivers *into* the client's message; it does not deliver *to* the client.
Replies are the client's own problem, and the usual shape is:

1. The external agent includes an address it can be reached at — its own
   session id, a callback URL, a queue name — in the `from` suffix.
2. The Leo agent reads that address out of the delivered
   `[message from <client>#<address>]` prefix and answers over whatever
   transport the client speaks.

That keeps Leo out of the business of knowing how any particular external agent
receives things. If you want a Leo agent to answer automatically, give it a
skill describing that transport.

A worked example — a containerized opencode agent whose plugin sends the
session id as the address, with the Leo agent replying via opencode's
`POST /session/<id>/prompt_async` — is written up in
[the design spec](../specs/2026-08-13-external-agent-messaging.md). The
container-side glue is deliberately not shipped here: it tracks another tool's
unversioned API, and Leo should not carry that.
