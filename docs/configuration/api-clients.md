# API Clients

An **API client** is an agent Leo does not supervise — typically running in a
container, on another machine, or inside CI — that needs to message one Leo
agent. It authenticates with a bearer token of its own, scoped to exactly that.

```yaml
api_clients:
  docker-scout:
    can_message: [rocket]
```

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
```

This writes the `api_clients` entry, generates a token at
`~/.leo/state/clients/docker-scout.token` (mode 0600), and prints it once.

```bash
leo client list          # names, allowed targets, whether the token file exists
leo client rm docker-scout
```

Both `add` and `rm` require a daemon restart to take effect. That cuts both
ways: **`leo client rm` does not revoke immediately** — the running daemon
holds the old token in memory and keeps accepting it until you restart it.

## What the token can do

Exactly one thing:

```
POST /web/agent/<target>/message
```

…where `<target>` matches `can_message`. Everything else is refused with 403 —
every `/api/*` route, every other agent verb (`interrupt`, `send`, `stop`), the
whole browser UI, and `/login`. The check runs before the request reaches the
middleware the operator and agent tokens use, so there is no path by which a
client token falls through to them.

`can_message` entries match the literal path segment, exactly or as a glob
(`scout-*`). Unlike [template permissions](permissions.md), **an empty list
denies everything** and is rejected at config load. The inversion is
deliberate: an empty allowlist meaning "unrestricted" is a safe default for an
agent Leo spawned, and the wrong one for a token living outside Leo.

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
  Host check rejects the request *before the token is looked at*.

```yaml
web:
  enabled: true
  port: 8370
  bind: 0.0.0.0
  allowed_hosts: [host.docker.internal, 10.0.2.10]
```

`leo client add` prints a reminder when the current config would not be
reachable. If a client is getting 403 on a target you know is allowed, check
these before suspecting the token.

## Worked example

A containerized opencode agent that messages one Leo agent and receives replies
in the session that sent them: see `contrib/opencode-leo-plugin/` and
[the design spec](../specs/2026-08-13-external-agent-messaging.md).
