# Plan: client chat post

Spec: `docs/specs/2026-10-21-client-chat-post.md` (it wins any conflict). Branch `feat/client-chat-post`. I checked every extension point against the code. Corrections:
- Socket `POST /config/reload` (`daemon/handlers.go:230`) does **not** call `ReloadConfig`. It only runs `scheduler.Install`.
- `StartWeb` runs after the socket is already serving (`service/process.go:1063`, then `:1093`), so `ReloadConfig` must not read `s.webServer` without synchronization.
- `migrate_test.go:184` hard-codes `user_version == 4`.
- The e2e suite has no `leo client` coverage, so this plan adds no e2e check.

All touched packages already have a `TestMain`, and the plan creates no new package. Avoid narrowing int conversions (gosec G115).

## 1. Config: `can_post` (`internal/config/api_clients.go`)
- Add `CanPost []string \`yaml:"can_post,omitempty"\``.
- `validateAPIClients`: an entry needs at least one of the two lists. The error becomes "must set can_message or can_post".
- Extract `validateClientPatterns(name, field, list)` and use it for both lists: no empty entries, and `leotools.ValidPattern`.
- RED (`api_clients_test.go`): a post-only client is valid; a client with neither list fails and the error names both; an empty `can_post` entry or a bad glob fails with `can_post[i]` in the message.
- Verify: `go test -race ./internal/config/`

## 2. Sender column and JSON (`internal/chat/migrate.go`, `model.go`, `store.go`)
- Migration `{version: 5, sql: "ALTER TABLE messages ADD COLUMN sender TEXT NOT NULL DEFAULT '';"}`.
- `Message.Sender string \`json:"sender,omitempty"\``.
- Add `sender` to `messageColumns`, `scanMessage` and the message INSERT.
- Change `migrate_test.go:184` to `schemaVersion`.
- RED tests:
  - `TestOpenStoreUpgradesAV4DatabaseAddsSender`: run `migrate(raw, migrations[:4])`, insert a row, `OpenStore`. Then `user_version == 5`, the old row has `Sender == ""`, and a new row with `Sender: "client:pipe"` round-trips through `Message()` and `Page`.
  - `model_test.go`: `"sender"` is present when set and absent when empty.
- Verify: `go test -race ./internal/chat/`

## 3. Chat service post-as-client (`internal/chat/outbound.go`)
- Add `const SenderClientPrefix = "client:"`, `type ClientPostRequest struct{ Agent, Client, Text string }` and `func (s *Service) PostFromClient(ctx, req) (Message, error)`. It:
  - runs enter/leave;
  - returns `ErrInvalid` when the trimmed text is empty or longer than `MaxTextBytes`;
  - returns `ErrUnknownAgent` when `!s.knownAgent(req.Agent)`, which maps to 404;
  - stores `Message{Agent, DirectionOut, KindText, Text, Sender: prefix+Client}`.
- No `withRun`, no task, no Deliver.
- Extract the shared insert → `Store.Message` → `publish(EventMessage)` tail of `Post` into `s.storeOutbound(m, files)`, and use it in both.
- RED (`service_test.go`): the message is stored out/text with the sender and publishes one EventMessage; the Deliver fake records zero calls; unknown agent → `ErrUnknownAgent`; whitespace-only or oversized text → `ErrInvalid`.
- Verify: `go test -race ./internal/chat/`

## 4. Client route and stamped sender (`internal/web/auth_client.go`)
- `ClientPolicy.CanPost []string`. Split `allowsTarget` into `allowsMessage` and `allowsPost`, both over a shared `matchesAny(patterns, target)`. An empty list stays deny-all, and each scope stays independent.
- Replace `clientMessageTarget` with `clientRoute(path) (target, verb string, ok bool)`, where verb is `"message"` or `"chat/post"`. Match the `/chat/post` suffix first, then `/message`. Reject a target that contains `/`, is `.` or `..`, or is empty.
- `serveClient` checks, in this order:
  1. The method isn't POST, or the route doesn't parse → 403. The error names both routes.
  2. The verb's scope check fails → 403. Scope is checked before existence, so a client can't probe for agents outside its scope.
  3. For `message`: today's path, unchanged.
  4. For `chat/post`: decode with `http.MaxBytesReader(maxChatJSONBody)`; a bad `from` → 400; `chatAvailable` → 503; `s.chat.PostFromClient({target, policy.Name, sanitizeClientText(text)})`; errors via `writeChatError`; success `200 {"ok":true,"data":{"id":…}}`.
- `chat/post` is **not** registered on the mux. It is served only from `serveClient`.
- RED:
  - `auth_client_parse_test.go`: table for `clientRoute`, covering `/web/agent/a/chat/post`, `/a/b/chat/post`, `/../chat/post`, `/a/chat/post/x` and `/a/chat/message`.
  - New `auth_client_post_test.go`, using a `newChatServer`-style helper with `Clients`:
    - a post-only client gets 200, and history shows `sender: "client:pipe"`;
    - `chatDeliveries` stays empty, and `s.injectPrompt` and `s.deliverPeer` are set to `t.Fatal`;
    - a disallowed target, `/message`, the `/api/*` list from `auth_client_token_test.go:81` and GET each give 403;
    - a `can_message`-only client gets 403 on `chat/post`;
    - a bad `from` and empty text give 400;
    - an in-scope (glob) unknown target gives 404;
    - an unknown token gives 401;
    - the operator token on the path gives 404.
- Verify: `go test -race ./internal/web/`

## 5. Push body prefix (`internal/push/notifier.go:263-286`)
- In `chatMessage`, when `m.Sender` has `chat.SenderClientPrefix`, the body becomes `"<name>: " + chatBody(m)`. The title stays the agent name.
- RED (`notifier_test.go`): a client message pushes the body `pipe: cam offline`, and an agent message is unchanged.
- Verify: `go test -race ./internal/push/`

## 6. Hot-swap on the web side (`internal/web/web.go:129,504`, `auth_client.go:89`)
- **Concurrency:** use `clients atomic.Pointer[[]ClientPolicy]`, not an RWMutex.
  - Each swap stores a freshly built slice that is never changed afterwards.
  - `lookupClient` does one `Load()`, so reads take no lock, and a single request sees exactly one snapshot.
  - A nil pointer means no clients.
- `New` stores `validClients(...)`. `func (s *Server) SetClients(cs []ClientPolicy)` runs `validClients(cs, s.apiToken, s.agentToken)` and then stores the result.
- RED (`auth_client_token_test.go`):
  - a client added through `SetClients` after `New` reaches its route;
  - a client dropped by a later `SetClients` gets 401;
  - a `-race` test sends requests from goroutines while `SetClients` runs in a loop with small counts.
- Verify: `go test -race ./internal/web/`

## 7. Daemon reload (`internal/daemon/server.go:447-463,548`, `handlers.go:230`)
- Extract `clientPolicies(cfg) []web.ClientPolicy` from StartWeb's loop, with `CanPost` added. Both StartWeb and reload use it.
- Add an interface `clientSetter interface{ SetClients([]web.ClientPolicy) }`, held in `s.clientSink atomic.Pointer[clientSinkHolder]`. StartWeb sets it the same way `setControl` does.
- `ReloadConfig`:
  1. `config.Load`; on error, return it and keep the old policies.
  2. Swap the client policies when the sink isn't nil. This runs **before** the opencode and scheduler steps, so an unrelated reload error can't block a revocation.
  3. The existing steps follow unchanged.
- `handleConfigReload` now calls `s.ReloadConfig()` and maps its error to a 500. This makes `leo service reload` and the socket path match the spec.
- RED (`reload_clients_test.go`, using a fake setter):
  - a client added to `leo.yaml` after `New` reaches the setter with `CanPost`;
  - a removed client is missing from the next set;
  - socket `POST /config/reload` through `s.Handler()` triggers the setter;
  - a nil sink means no panic.
- Verify: `go test -race ./internal/daemon/`

## 8. CLI (`internal/cli/apiclient.go`, `delegation.go:171`)
- `add`:
  - Add `--can-post` as a repeatable `StringSliceVar`. At least one of `--can-message` or `--can-post` is required. Patterns are validated by `saveConfig` → `Validate`.
  - Save `CanPost`.
- Extract `reloadIfRunning(cmd, cfg) (reloaded bool, err error)` from the tail of `saveDelegationAndReload`, and reuse it there.
- `add` order: save → `EnsureClientToken` → reload. `rm` order: delete → remove the token → reload.
- Messages:
  - reloaded: "active now" for add, "revoked" for rm;
  - daemon not running: "applies when the daemon next starts";
  - reload error: return an error saying the change was saved but is **not active / not revoked**, and to run `leo service reload`.
- Remove the "Restart the daemon" and "NOT YET REVOKED" text. `list` gains a CAN POST column.
- RED (new `apiclient_test.go`, `-c` config, short `os.MkdirTemp` home, and `startStubDaemonSocket` from `agent_worktree_test.go:22`, which must answer both `/health` and `/config/reload`):
  - neither flag gives an error and no write;
  - two `--can-post` flags are saved;
  - a bad glob is rejected and the config is unchanged;
  - with a stub daemon, add and rm each POST `/config/reload` once;
  - with no daemon, no error and the next-start note is printed;
  - a stub returning 500 makes rm return an error.
- Verify: `go test -race ./internal/cli/`

## 9. Docs and CHANGELOG
- `docs/configuration/api-clients.md`: `can_post`, the endpoint, the error codes, the reload behavior.
- `config-reference.md`: `can_post`.
- `chat.md`: the message `sender` field.
- `CHANGELOG.md` Unreleased → Added:
  - `can_post` and `leo client add --can-post`;
  - the endpoint contract: `POST /web/agent/<target>/chat/post`, Bearer client token, `{"from","text"}`, `200 {"ok":true,"data":{"id"}}`, 401/403/400/404, no turn injected;
  - message `sender` (`client:<name>`);
  - push body prefix;
  - client policies hot-reload, and `client add`/`rm` reload a running daemon.
- Changed: socket `/config/reload` now runs the full `ReloadConfig`.

## 10. Integration gate
- Check load with `uptime`. If the load average is at or below ~30, run `make test` and `make lint`. If it's higher, defer those to merge.
- Hand off for a fresh-context `review.security` (the diff is auth-scoped).

## 11. Live verification (isolated daemon; never production, never `leo service restart`)
1. Run `make build`, then `mkdir /tmp/leochat`. `leo.yaml` gets `web: {enabled: true, port: 8372, bind: 127.0.0.1}` and one template.
2. Start the daemon with Bash `run_in_background`: `env -i HOME=$HOME PATH=$PATH LEO_HOME=/tmp/leochat ./bin/leo service --supervised --config /tmp/leochat/leo.yaml >/tmp/leochat/daemon.log 2>&1`. Wait until `/tmp/leochat/state/leo.sock` answers.
   - Tell Evan that a test daemon strips the roster lines from production sessions.
3. Spawn one agent `pipe-cam` from the template with `-c /tmp/leochat/leo.yaml`, then stop it. A stopped record still counts as a known agent.
4. Every CLI call passes `-c /tmp/leochat/leo.yaml`, because `LEO_HOME` doesn't isolate the CLI. Run `./bin/leo client add pipe --can-post 'pipe-*' -c …` and confirm it says active now, with no restart. Capture the token.
5. `curl -sX POST localhost:8372/web/agent/pipe-cam/chat/post` with `-H "Authorization: Bearer $TOK"` and the body `{"from":"pipe","text":"cam offline"}` → 200 with an id.
6. `curl localhost:8372/api/v1/chat/pipe-cam/messages` with `-H "Authorization: Bearer $(cat /tmp/leochat/state/api.token)"`. The newest message has `"sender":"client:pipe"`.
7. With the client token:
   - `/web/agent/pipe-cam/message` → 403;
   - `/api/v1/state` → 403;
   - `/web/agent/other/chat/post` → 403;
   - `/web/agent/pipe-ghost/chat/post` → 404.
8. `./bin/leo client rm pipe -c …`, then repeat step 5 → 401.
9. Teardown:
   - `pkill -f "service --supervised --config /tmp/leochat/leo.yaml"`;
   - kill the test tmux session by exact quoted name;
   - `trash /tmp/leochat`;
   - confirm production is untouched with `leo agent list`.
10. After merge, the orchestrator (not the implementer) tells the leoterm agent about `sender` through `leo_send_message`.

## Ambiguities
- **Nullable `sender` vs. repo convention.** The spec says nullable; every other column is `NOT NULL DEFAULT ''`. Assumption: `NOT NULL DEFAULT ''`, with JSON `omitempty`. The wire result is the same, and "empty" for agent messages.
- **Sanitization.** The spec says "sanitized like `/message`". `sanitizeClientText` flattens newlines, which only matters for tmux turn submission. Assumption: apply it literally, so multi-line pipeline notifications become one line. Flag this to Evan.
- **Unknown agent status.** The agent tool route answers 400 for an unknown agent. The client route answers 404 via `ErrUnknownAgent`. The scope 403 is checked first.
- **Route reachability.** The spec doesn't say who else may use the path. Assumption: it's client-only; operator and agent tokens get 404, so there's no new operator surface.
- **"Under a lock".** Assumption: the `atomic.Pointer` swap satisfies it, since readers see one immutable snapshot.
- **`/config/reload` behavior change.** It is unified through `ReloadConfig`, so socket reloads now also refresh the opencode context. Assumption: that's intended, since the spec names that path.
- **Boot window.** A reload between the start of socket serving and the sink being set skips the swap, and StartWeb keeps its boot-time config. Accepted risk, since the window is milliseconds at boot.
- **Task association.** Assumption: client posts never attach to an agent's in-flight persistent task (`withRun`).
- **Push filtering.** The `#session` suffix is accepted and discarded. The push prefix uses the bare name, and the agent's existing chat push filter still applies.
- **`client list`.** The CAN POST column isn't in the spec. Assumption: include it, because otherwise a post-only client shows an empty scope.

## Orchestrator decisions
- **Sanitization:** chat/post keeps `\n` and `\t`. It strips other control characters, using the same control-char rules as `/message` minus the newline flattening. Chat is rendered Markdown, not tmux input, and multi-line recaps must survive. This is a deliberate refinement of the spec's "sanitized like /message".
- All other ambiguities are resolved as the planner assumed.
- The review uses the `review.security` role.
