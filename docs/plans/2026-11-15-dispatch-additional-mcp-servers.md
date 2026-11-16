# Plan: per-dispatch `additional_mcp_servers`

Spec: `docs/specs/2026-11-15-dispatch-mcp-servers.md` is the source of truth. This builds on #338 (`docs/specs/2026-11-14-claude-mcp-allowlist.md`, `internal/harness/claude/mcp_servers.go`).

## Traced call path
- **MCP tool:** `internal/mcp/tools.go:506` (schema) and `:556-571` (args to `consult.Request`) → `internal/mcp/client.go:259` `daemonClient.dispatch` (builds the JSON body).
- **API:** `internal/web/handlers_consult.go:~30-103` (request struct, 400 checks, `consult.Request{…}`). The consult handler at ~391 is left alone.
- **Launch:** `consult.Dispatcher.Start` (`internal/consult/consult.go:350`) → `resolveClaudeDispatchOptions` (`internal/consult/claude_profile.go:58`) → `claudeharness.ApplyMCPServers` → `materializeMCPConfig` (`consult.go:601`).
- **Interactive:** `interactive.go:325` builds `LaunchRequest` from the record (`placementRecord`). The first launch and a reopen both use it. `TmuxInteractiveRuntime.Launch` (`interactive_runtime.go:150`) then re-resolves the options.
- **Headless continuation:** `continuation.go:82-84`. The template's options are re-resolved; the environments come from `rec.InheritedEnvironments`.
- **Reopen:** `reopen.go:116` plans it, then it relaunches through `interactive.go:325` with `Reopen: true`.
- **Codex and opencode:** `decoded` is not `claudeharness.Options`, so they take `headlessChildMCP` / `withLeoMCPBridge` (`child_mcp.go`) and never call `ApplyMCPServers`.
- **Warnings:** `Record.Warnings` / `Started.Warnings` → `logDispatchWarnings`, the tool text (`tools.go:583`), the CLI run (`cli/dispatch.go:176`) and the `watchConsult` header (`cli/consult.go:216`).
- **CLI:** `newDispatchRunCmd` (`cli/dispatch.go:90-204`). `leo dispatch show` JSON-encodes the whole `Record`.
- **Precedent:** `environments` already runs this exact path (tool → client → handler → Request → Record → continuation/reopen). Mirror it.

## Steps

### 1. Validation and normalization (consult)
- Add `internal/consult/additional_mcp.go` with `NormalizeAdditionalMCPServers(names []string) ([]string, error)`. It removes duplicates in first-seen order.
  - Each name must match `^[A-Za-z0-9_.-]{1,64}$`.
  - At most 16 names after dedupe.
  - Errors name the bad entry or the count. `nil` in gives `nil` out.
- Failing test first: `TestNormalizeAdditionalMCPServers` (table) in `internal/consult`. Cases: nil, dups, exactly 16, 17, `a/b`, empty string, 65 characters, `.`.
- Verify: `go test -race -run TestNormalizeAdditionalMCPServers ./internal/consult/`

### 2. Request and Record fields, effective-set merge, headless Start
- Add `Request.AdditionalMCPServers []string` and `Record.AdditionalMCPServers []string` (`json:"additional_mcp_servers,omitempty"`).
- `Start` normalizes again for defense in depth (`invalidf` → ValidationError), then stores the normalized list on the record.
- `resolveClaudeDispatchOptions(..., additional, ...)` gets a new `additional` parameter:
  - After the profile, if `o.MCP == "none"`, set `o.MCPServers = dedupe(o.MCPServers ++ additional)` on the copy.
  - If `o.MCP == "inherit"` and `additional` is non-empty, append the warning `additional_mcp_servers ignored under mcp: inherit`.
  - A nil `additional` must leave `o` exactly as it was.
- Update all three call sites. `Start` passes `req.AdditionalMCPServers`; the other two are done in step 3.
- Failing tests in `internal/consult/mcp_servers_dispatch_test.go`, reusing `newMCPDispatchFixture`, `configServerNames` and `assertArgvHasOnlyMCPPath`:
  - `TestHeadlessDispatchAdditionalMCPServersOnlyAddsToBridge`: template has no `mcp_servers`, extras are `[chrome]`. The file holds exactly `{leo, chrome}`, is 0600, and argv carries only its path.
  - `TestHeadlessDispatchAdditionalMCPServersUnionWithTemplate`: template `[a]` plus call `[a, b]` gives `{leo, a, b}`.
  - `TestHeadlessDispatchAdditionalUnknownNameWarnsAndLaunches`: the warning is on `Started.Warnings` and `Record.Warnings`.
  - `TestHeadlessDispatchAdditionalIgnoredUnderInheritWarns`
  - `TestHeadlessDispatchWithoutAdditionalIsByteIdentical`
  - `TestStartRejectsInvalidAdditionalMCPServers`
- Verify: `go test -race -run 'MCPServers|Additional' ./internal/consult/`

### 3. Continuations and reopens rebuild the same set
- `continuation.go:84`: pass `rec.AdditionalMCPServers`.
- Add `LaunchRequest.AdditionalMCPServers` (`interactive.go:34`), set from `placementRecord.AdditionalMCPServers` at `:325`. That covers the first launch and the reopen.
- `interactive_runtime.go:152`: pass `req.AdditionalMCPServers`.
- Failing tests:
  - `TestHeadlessContinuationKeepsAdditionalMCPServers`: the resumed turn's file is `{leo, chrome}` and is removed afterwards.
  - `TestInteractiveStartUsesAdditionalMCPServers`
  - `TestInteractiveReopenKeepsAdditionalMCPServers`
- Verify: `go test -race -run 'Continuation|Reopen|Interactive.*MCP|Additional' ./internal/consult/`

### 4. Codex and opencode warn and launch
- In `Start`, when `decoded` is not claude options and the list is non-empty, append `additional_mcp_servers ignored: harness <name> does not support them`. Continuation and reopen don't repeat it; it is already on the record.
- Failing tests: `TestCodexDispatchAdditionalMCPServersWarnsAndLaunches`, plus an opencode variant.
- Verify: `go test -race -run 'Codex|Opencode' ./internal/consult/`

### 5. Daemon API boundary (400)
- `handlers_consult.go`: add an `AdditionalMCPServers` field (JSON `additional_mcp_servers`).
  - Normalize it. An error writes 400 `invalid additional_mcp_servers: <msg>`.
  - Pass the normalized list into `consult.Request`. The consult handler is untouched.
- Failing tests:
  - `TestDispatchHandlerRejectsTooManyAdditionalMCPServers` (17 → 400)
  - `TestDispatchHandlerRejectsSlashInAdditionalMCPServer` (→ 400)
  - `TestDispatchHandlerForwardsDedupedAdditionalMCPServers`
- Verify: `go test -race -run 'DispatchHandler.*Additional' ./internal/web/`

### 6. `leo_dispatch` MCP tool
- `tools.go:506`: add schema property `additional_mcp_servers`, an array of strings. Its description says it only adds to the template's list, names come from the run account's `mcpServers`, and unknown names are warnings.
- `:556`: parse it with `stringListArg` and put it on the Request.
- `client.go:259`: write the field only when it is non-empty.
- `leo_consult` and `leo_send_dispatch` stay unchanged.
- Failing test: `TestLeoDispatchForwardsAdditionalMCPServers` (new `internal/mcp/dispatch_additional_mcp_test.go`). It asserts the field is sent when given and absent when omitted.
- Verify: `go test -race -run TestLeoDispatchForwardsAdditionalMCPServers ./internal/mcp/`

### 7. CLI flag and visibility
- `cli/dispatch.go`: add `StringArrayVar(&additionalMCP, "additional-mcp-server", nil, …)`. It is repeatable and commas are not split. When set, write `body["additional_mcp_servers"]`.
- `cli/consult.go` `watchConsult`: when the list is non-empty, print `additional MCP servers: a, b` after the header, before the warnings.
- `leo dispatch show` already gets the field through JSON.
- Failing tests: `TestDispatchRunSendsAdditionalMCPServers`, `TestWatchHeaderShowsAdditionalMCPServers`.
- Verify: `env -u TMUX go test -race -run 'Additional' ./internal/cli/`

### 8. Docs (no TDD)
- `docs/configuration/dispatches.md`: a section on `additional_mcp_servers` and `--additional-mcp-server` covering:
  - it only adds to the template's list;
  - name resolution, as in #338;
  - the limits;
  - the inherit, codex and opencode warnings;
  - that it persists across follow-ups;
  - in plain terms: read-only roles get no exception. Their harness permissions still block file writes, but an added server can act on external systems.
- `docs/configuration/harnesses.md:179`: one cross-reference sentence.

### 9. Integration gate (once, before PR)
- Check the load average first, then run:
  - `make test`
  - `make lint`
  - `env -i HOME=$HOME PATH=$PATH TMUX_TMPDIR=$(mktemp -d) make e2e`
- Fresh-context `review.security`, because the change touches launch config that can carry secrets.
- Live check on an isolated test daemon: one dispatch with `[chrome]`, then a follow-up. Check the server set in `state/mcp/<id>.json` and the file's mode.

## Acceptance → step
| Spec acceptance | Step |
|---|---|
| `[chrome]` with no template list → bridge + chrome, 0600, argv carries only the path | 2, 6 |
| Template `[a]` + call `[a, b]` → bridge, a, b | 2 |
| Unknown name → warning, run launches | 2 |
| Follow-up → same effective set | 3 |
| Codex → launches with a warning | 4 |
| 17 names, or a `/` → 400 | 1, 5 |
| No extras → byte-identical to v0.83.0 | 2, plus the #338 "unchanged" tests |

## Decisions (open questions resolved by orchestrator)
1. The record and watch header show the *requested* list. Skipped names appear as warnings below it. No second "resolved" field.
2. The 16-name limit counts after dedupe.
3. Unknown extras reuse #338's `mcp_servers: "x" is not defined…` warning text.
4. No marker on `leo dispatch list` rows. "Status" in spec §8 is satisfied by `show` + `watch`.

## Risks
- A new CLI or tool talking to an older daemon gets a 400 only when it sends the field, because the field is omitted when empty. Acceptable; note it in the PR.
- Interactive runs log the warnings twice, once at Start and once at Launch. This already happens with #338.
- An extra named `leo` hits #338's reserved-name warning. That is correct.
- The record stores the requested names, so later `.claude.json` edits change what a follow-up loads. This is consistent with #338.
