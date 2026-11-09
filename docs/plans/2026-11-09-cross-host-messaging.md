# Implementation plan: cross-host agent messaging

I checked every starting point in the brief against the code. Two parts of the spec can't be built as written; both are in §5 (Q1, Q2). Steps 6 and 8 assume my recommended answers.

## 1. Ordered steps

**S1. Config fields and validation.**
- Files: `internal/config/config.go` (`HostConfig`, `Validate`) and `secret_refs.go` (`SecretReferences`).
- `HostConfig` gains:
  - `APIURL string \`yaml:"api_url,omitempty"\``
  - `APIToken *EnvValue \`yaml:"api_token,omitempty"\`` (reuses the `EnvValue` literal-or-secret-ref type)
  - `APIClient string \`yaml:"api_client,omitempty"\`` (see Q2)
- `Validate` adds these checks:
  - `api_url` uses http or https, has a host, and has no userinfo, query or fragment.
  - `api_url` and `api_token` are set together or not at all.
  - A ref token passes `Ref.Validate`.
  - When `api_url` is set, the host key must not contain `@`.
  - `api_client` must pass the same name rule as `api_clients` names.
- `SecretReferences` also returns `client.hosts.<h>.api_token` (scope `client.hosts.<h>`, key `api_token`), so `leo validate` checks it.
- Failing tests: `TestValidate_ClientHostAPIFields` (table) and `TestSecretReferences_IncludesHostAPIToken` in `internal/config`. Add a byte-identical round-trip case for a config without these fields.
- Verify: `go test -race -run 'TestValidate_ClientHost|TestSecretReferences' ./internal/config/`

**S2. Web schema and config API round-trip.**
- Files: `internal/web/schema/registry.go`, `internal/web/config_resources.go`.
- Register `api_url` and `api_client` under `SectionClientHost`.
- Add `api_token` to `Excluded` as YAML-only. The UI must never render a token.
- `registry_drift_test` fails first by itself, which is the gate.
- New test `TestConfigHosts_EditPreservesAPIToken` (`internal/web`): saving a host from the form must not drop `api_token`. The current `put` replaces the whole `HostConfig`.
- Verify: `go test -race -run 'Drift|TestConfigHosts' ./internal/web/...`

**S3. Parse the target.**
- New `internal/mcp/remote.go`: `parseTarget(to string) (agent, host string, remote bool, err error)`.
- Failing test: `TestParseTarget` covers bare `a`, `a@h`, `@h`, `a@`, `a@b@c` and whitespace.
- Verify: `go test -race -run TestParseTarget ./internal/mcp/`

**S4. Sender-side permission matching.**
- File: `internal/leotools/leotools.go` (`AllowsMessage`).
- When the target contains `@`, only entries that contain `@` can match. Otherwise an existing `coder-*` would match `x@laptop`, because `path.Match`'s `*` matches `@` (see R1).
- An empty list stays unrestricted.
- Failing test: `TestAllowsMessage_RemoteTargets` (`internal/leotools`).
- Verify: `go test -race -run TestAllowsMessage ./internal/leotools/`

**S5. Daemon route that resolves a host.**
- New `internal/web/handlers_remote_host.go`: `GET /api/remote-host/{host}`, accessible with the agent token.
- It loads config with `loadConfigForAPI` (so edits apply without a restart) and resolves the token with `config.Expander`. This needs a small new `Expander.ResolveValue(scope, key string, v EnvValue) (string, error)` built on `LiteralLayer` + `ResolveLayer`.
- Responses:
  - 404 `unknown host "h"`
  - 404 `host "h" has no api_url`
  - 502 for a secret-resolution failure, with an error that names the ref and never the value
- Never logs the body.
- Failing test: `TestRemoteHostRoute` (`internal/web`) covers the known, unknown, no-URL, secret-ref, and client-token-rejected cases.
- Verify: `go test -race -run TestRemoteHostRoute ./internal/web/`

**S6. Remote send in the MCP server.**
- `internal/mcp/remote.go` adds `remoteClient` with its own `http.Client` (10 s timeout, `CheckRedirect` returning `ErrUseLastResponse`).
- `daemonClient.remoteHost(host)` calls S5.
- `remoteClient.send(ctx, h remoteHost, agent, from, text)` POSTs to `<api_url>/web/agent/<PathEscape(agent)>/message` with body `{"text": <raw message>, "from": "<api_client>#<processName>"}`. The text has no local prefix because the receiver adds one.
- Wiring: the `leo_send_message` handler in `tools.go` (~413) branches on `parseTarget`:
  - The permission check runs on the full `agent@host` string.
  - A dispatch child (`processName` starts with `dispatch:`) gets a tool error: `:` fails `validSessionSuffix`. See Q4.
- `mapRemoteError(host string, status int, body []byte, err error) error`:
  - 401: `host h rejected the api_token`
  - 403: `not permitted to message a on h: <receiver error>`
  - 404: `no agent a on h`
  - 400: passes the receiver's message through
  - connection refused or DNS failure: `host h unreachable at <api_url>`
  - timeout: `host h did not answer within 10s`
  - Errors never include the token. Assert that in a test.
- Inject the remote client with a `withRemote(rc)` registry option, so tests swap transports.
- Failing tests (`internal/mcp`): `TestMapRemoteError` (table) and `TestSendMessage_Remote_RequestShape`, which asserts URL, `Authorization`, and body.
- Verify: `go test -race -run 'TestMapRemoteError|TestSendMessage_Remote' ./internal/mcp/`

**S7. Discovery.**
- Receiver: `serveClient` in `internal/web/auth_client.go` gets a `GET /web/client/agents` route that never reaches the mux. It returns `[{name, status}]` from `agentSvc.List()`, filtered by `policy.allowsMessage`. The response is the same 200 `[]` whether `can_message` is empty or nothing matches, so it can't be used to probe.
- Sender: `leo_list_agents` gains an optional `host` parameter, and `remoteClient.listAgents(ctx, h)` maps errors like S6.
- Failing tests: `TestServeClient_ListAgentsFiltered` (`internal/web`) and `TestListAgents_RemoteHost` (`internal/mcp`).
- Verify: `go test -race -run 'ListAgents' ./internal/web/ ./internal/mcp/`

**S8. Render the sender as `agent@host` on the receiver.**
- File: `serveClientMessage` in `internal/web/auth_client.go`.
- When the policy has `peer: true` (new `APIClientConfig.Peer bool \`yaml:"peer,omitempty"\``, carried on `ClientPolicy`, see Q1), render `[message from <suffix>@<name>]`. All other clients keep `name#suffix`.
- Failing test: `TestServeClientMessage_PeerRendersAgentAtHost` (`internal/web`), including a non-peer case that stays unchanged.
- Verify: `go test -race -run TestServeClientMessage ./internal/web/`

**S9. Integration test (§3), then the full gate.**
- Run `make test`, `make lint`, and `make e2e` (wrapped in `env -i`, per memory) at the merge gate.
- Expected to run long: the full suite under `heavy` in the background.

## 2. Interfaces

- **YAML (sender side):** `client.hosts.<h>.api_url`, `.api_token` (string or `{secret: …}` mapping), `.api_client` (defaults to the lowercased short hostname).
- **YAML (receiver side):** `api_clients.<name>.peer: true`.
- **Go, config:** `func (x Expander) ResolveValue(scope, key string, v EnvValue) (string, error)`.
- **Go, MCP:**
  - `type remoteHost struct{ Name, URL, Token, ClientName string }` (the S5 response)
  - `func (c *daemonClient) remoteHost(name string) (remoteHost, error)`
  - `type remoteClient struct{ http *http.Client }`
  - `func (r *remoteClient) send(ctx context.Context, h remoteHost, agent, from, text string) error`
  - `func (r *remoteClient) listAgents(ctx context.Context, h remoteHost) (json.RawMessage, error)`
  - `func withRemote(rc *remoteClient) registryOption`
- **HTTP:**
  - `GET /api/remote-host/{host}` returns `{api_url, api_token, api_client}` (agent or operator token).
  - `GET /web/client/agents` returns `[{name, status}]` (client token only).

## 3. Integration test (`internal/mcp/crosshost_integration_test.go`, package `mcp_test`)

Neither `internal/web` nor `internal/mcp` imports the other (checked with `go list -deps`), so the external test package can import both.

**Setup**
- `export_test.go` exposes `newRegistry` and `withRemote`.
- **Receiver:** a real `web.New(...)` behind `httptest.Server`, with `Clients: [{Name: "dionysus", Token: T, CanMessage: ["rocket"], Peer: true}]`, a stub `agentSvc` listing `rocket` and `infra`, and a capturing message seam (or a stub `ResolveHandle` driver) that records the delivered text.
- **Sender daemon:** a real `web.New` with `client.hosts.laptop = {api_url: receiver.URL, api_token: T, api_client: "dionysus"}`.
- **Sender:** the MCP registry on that daemon with `processName = "scout"`.
- **Wire capture:** a `RoundTripper` around the remote client records the request.

**Cases**
1. **`rocket@laptop` is delivered.**
   - Captured request:
     - Method: `POST`
     - URL: `<receiver>/web/agent/rocket/message`
     - Header: `Authorization: Bearer T`
     - Body: exactly `{"from":"dionysus#scout","text":"hi"}`
   - Delivered text: `[message from scout@dionysus] hi`.
2. **`infra@laptop` is denied.** Tool error contains `not permitted`. Nothing is delivered.
3. **`leo_list_agents{host:"laptop"}`** returns only `rocket`, with no fields beyond name and status.
4. **Wrong token** gives a 401 mapped error that does not contain T.
5. **Closed listener** gives `unreachable`.

## 4. Docs

- `docs/configuration/api-clients.md`: the `peer` flag; a "Leo-to-Leo peers" section with the full two-host walkthrough (Dionysus ⇄ laptop): tokens via `leo client`, `api_clients` entries, `can_message` globs, `web.bind` + `allowed_hosts` for LAN reach, and the reply path; add `GET /web/client/agents` to "What the token can do".
- `docs/guides/remote-cli.md`: `api_url`, `api_token` and `api_client` on hosts, with a pointer to the api-clients walkthrough.
- Config reference: the new keys. MCP tool descriptions: `agent@host` and `host`.
- Note that remote text is flattened to one line, because the receiver's `sanitizeClientText` strips newlines.

## 5. Risks and open questions

- **Q1 (spec gap): rendering on the receiver.** Today the receiver prints `[message from dionysus#rocket]`, and the docs promise that form to external clients. Rendering `rocket@dionysus` for every client would change behavior for existing users. I recommend an opt-in `api_clients.<n>.peer: true`, a receiver-side field that clients can't assert themselves. The spec said "config and docs only" for the receiver, so this needs sign-off.
- **Q2 (spec gap): the sender's own host name.** Nothing in config holds it. I recommend a per-host `api_client` field defaulting to the lowercased short `os.Hostname()`. The alternative is a single global `client.name`.
- **Q3: where the token is resolved.** The MCP process doesn't load `leo.yaml`, and `LEO_CONFIG` is only set for dispatch children. I recommend the S5 daemon route: it works the same on claude, codex and opencode, and needs no new environment variable on every launch path. Any agent holding the agent token can read a host's token. That's the same trust as reading `leo.yaml` and running the secret command as the same user, so I'd accept it. The alternative is adding `LEO_CONFIG` to every MCP launch path and the codex bridge env list.
- **Q4: dispatch children can't send cross-host.** Their name `dispatch:<id>` fails `validSessionSuffix`. I recommend refusing with a clear tool error rather than widening the suffix charset.
- **R1: glob widening.** Without the S4 rule, existing local globs would start matching remote targets. S4 is a fail-closed tightening and only affects targets containing `@`.
- **R2: web form wiping the token.** The UI's host `put` could silently drop `api_token` on save; the S2 test guards this.
- **R3: follow-lead.** The export says it never resolves a client host. Confirm `client.hosts` isn't synced; if it is, the secret-ref rules apply.
- **R4: macOS Local Network permission.** This is spec default 4: the request comes from the MCP process. Verify in the live laptop test; the daemon-side S5 call is loopback only.
- **R5: redirects and token leakage.** A misconfigured `api_url` that redirects must not carry the bearer header; `CheckRedirect` refuses redirects to prevent that.
