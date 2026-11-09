# Cross-host agent messaging

Status: draft · 2026-11-09

## Goal

An agent on one leo host can message an agent on another leo host, e.g. an agent on Dionysus messaging an agent on Evan's laptop, with the same `leo_send_message` tool it uses locally. The receiving agent can reply the same way.

## Non-goals

- Queueing or retrying when the remote host is offline (the laptop sleeps). The send fails and the agent sees the error.
- SSH transport, relays, or reverse connections. The receiving daemon must be reachable over HTTP.
- Merging remote agents into the default `leo_list_agents` output.
- Cross-host `leo_dispatch`, chat, or any other tool.

## Design

### Addressing

- `to` in `leo_send_message` accepts `agent@host`. `host` is a key in the sender's `client.hosts`. A bare `agent` keeps today's local behavior, unchanged.
- An unknown host, or a host with no API endpoint configured, is a tool error that names the host. There is no fallback to local.

### Transport: the existing `api_clients` route

- The receiver needs no new message endpoint. The sending host is an `api_clients` entry on the receiver, named after the sending host (e.g. `dionysus`). It has its own bearer token and `can_message` globs, and is default-deny, as today.
- The sender's `client.hosts.<host>` gains two optional fields:
  - `api_url`: the base URL of the receiving daemon's web listener, e.g. `http://10.0.4.x:<port>`.
  - `api_token`: the bearer token the receiver issued. It uses leo's existing secret-reference mechanism, so the shared config never holds the token in plaintext.
- The `leo_send_message` tool runs inside the agent's MCP process, so that is where the remote request originates. It POSTs to the receiver's `api_clients` message route with `from = <sending-host>#<sender-agent>`. That form already passes `allowsFrom`, because the client name is the host name.
- The receiver enforces `can_message`. A 403 comes back to the sending agent as a clear "not permitted" error, and an unreachable host as a clear "host unreachable" error. The token is never echoed in any error or log.

### Sender identity and replies

- A remote message shows its sender as `[message from <agent>@<host>]`, where `<host>` is the `api_clients` name, so the recipient can reply to that exact string. Replying needs the reverse config: the laptop's `client.hosts.dionysus` with `api_url` and `api_token`, plus a `laptop` `api_clients` entry on Dionysus.
- The local sender check is unchanged: no self-sends, and the sender's template `can_message` still applies, matched against the full `agent@host` string.

### Discovery

- `leo_list_agents` takes an optional `host`. With `host`, it lists that daemon's agents, filtered to the ones the token's `can_message` allows. Without it, it behaves exactly as today.
- This needs one new read route on the receiver, authenticated by an `api_clients` token, that returns only the agents the token can message (name and status, nothing else).

### Receiver setup (config and docs only)

The receiving daemon must listen beyond loopback, which takes `web.bind` plus `allowed_hosts`. Document a two-host walkthrough (Dionysus ⇄ laptop) in `docs/configuration/api-clients.md` and `docs/guides/remote-cli.md`, covering tokens, globs, and reaching the laptop over the LAN.

### Resolved during planning

- The receiver renders `agent@host` only for `api_clients` entries marked `peer: true`, a new opt-in receiver field. Every other client keeps today's `name#suffix` form.
- The sender's own name comes from `client.hosts.<h>.api_client`. When unset it defaults to the lowercased short `os.Hostname()`.
- The agent's MCP process gets the host URL and token from a loopback daemon route that requires the agent token. The trust level is the same as reading `leo.yaml`.
- Dispatch children (`dispatch:<id>`) cannot send cross-host; they get a clear tool error.
- Remote message text arrives as a single line, because the receiver's existing sanitizer strips newlines.

## Compatibility

Every new config field is optional and additive. Existing configs, bare agent names, and the local send path behave exactly as before. External users need no migration.

## Testing

- Unit: parsing `to` (bare, `a@h`, empty parts, multiple `@`), resolving a host (unknown, no `api_url`), mapping errors (401, 403, 404, connection refused, timeout), and the shape of `from`.
- Integration (`httptest`, both sides real): a sender's MCP tool sends to a receiver daemon with an `api_clients` entry. Cover allowed delivery, a target the globs deny, and listing filtered by `can_message`. Assert the exact request (URL, auth header, body), not just the call count.
- Live: Dionysus → laptop, then a reply laptop → Dionysus.

## Defaults chosen (check these)

1. **Sender naming.** `api_clients` entries are named after the sending host, so the `<host>#<agent>` sender form works unchanged.
2. **Timeout.** A remote send times out after 10 s, and the agent is not retried.
3. **No offline queue.** If the laptop is asleep, the send fails and the agent retries later, or doesn't.
4. **Network permission.** The request originates from the agent's MCP process. macOS Local Network permission should come through the foreground-tmux grant from PR #137. Verify this in the live test on the laptop side.
