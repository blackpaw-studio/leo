# Client tools

Client tools let a connected app (the Leo Mac/iOS app, any API client) offer
tools to your agents. The daemon only relays: an agent calls a tool, the daemon
parks the call and publishes it on the event stream, the app that registered the
tool performs it and posts the result back. The daemon knows nothing about what
a tool does; names, schemas and results belong to the app.

Wire fixtures for every request and response live in
`internal/clienttools/testdata/wire/`.

## For apps

All routes are under `/api/v1/client-tools` (operator or paired-device token)
and on the daemon's unix socket. Responses use the usual `{ok, data, error, code}` envelope.

1. **Connect.** Open `GET /api/v1/events?client_tools=1` (operator token only;
   an agent token gets 403). The `hello` frame carries your `client_id`. Closing
   the stream withdraws everything you registered.
2. **Register.** `POST /api/v1/client-tools` with `{client_id, group, tools:
   [{name, description, input_schema, timeout_s}]}`. A group is replaced as a
   whole. `timeout_s: 0` means the call never times out (a handoff). Errors:
   `invalid_tool`, `too_many_tools` (64 per group and per daemon),
   `unknown_client` (404), `name_conflict` (409). Tool names cannot collide
   with Leo's own tools or another client's.
3. **Serve.** On a provider stream you receive `client_tool.request` events
   with the agent, tool and arguments. Claim it with
   `POST /requests/{id}/claim {client_id}`, then answer with
   `POST /requests/{id}/result {client_id, ok, content | error}`.
   `content` is an array of MCP content blocks (text, image). On (re)connect,
   `GET /requests?pending=1` lists what is still waiting. A call whose agent
   stops or whose tool is withdrawn arrives as `client_tool.cancelled`.
4. **Ownership.** The first claimant of a request owns that agent's group from
   then on (`409 owned_by_other` for a different client, `already_claimed`
   for a second claim). A second result is `409 already_settled`; a result from
   a non-claimant is `409 not_claimant`; an unknown or settled id is
   `404 unknown_request`.

Other event streams never see call arguments: `client_tool.request` and
`client_tool.cancelled` arrive redacted to ids, so `seq` stays contiguous.
`client_tools.changed` (revision and tool names) goes to everyone. The `hello`
feature list includes `client_tools`.

## For agents

Each agent's MCP server shows the tools according to the harness:

- **Claude Code and opencode** re-list tools on `notifications/tools/list_changed`,
  so every registered client tool appears as a native tool and the server
  notifies (coalesced to one second) when the set changes.
- **codex and unknown harnesses** do not re-list in a session, so they get two
  fixed tools: `leo_app_tools` (list what apps offer) and `leo_app_call
  {tool, args}`. They are listed only if an app has ever registered with this
  daemon, sampled once when the session first lists tools; an app registering
  for the first time needs an agent restart to be seen.

Results pass through verbatim, images included; failures come back as tool
errors `"<code>: <message>"` (`no_client`, `timeout`, `cancelled`, or the app's
own). `deny_tools` can name a client tool or either fallback tool; the names of
tools apps have ever registered are remembered in `~/.leo/state/client-tools.json`
so a `deny_tools` entry stays valid at boot before the app reconnects.
Which harness honors `list_changed` was measured by `scripts/harness-probe`
(`internal/mcp/harnesscaps.go`).
