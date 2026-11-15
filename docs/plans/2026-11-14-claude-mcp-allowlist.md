# Plan: Claude per-template MCP allowlist (`mcp_servers`)

Spec: `docs/specs/2026-11-14-claude-mcp-allowlist.md` (source of truth). Branch `feat/claude-mcp-allowlist`.

## Call sites (confirmed)
Strict MCP argv is built in one place: `strictMCPArgs` (`internal/harness/claude/args_shared.go:9`). It is called from agent argv (`args.go:50`) and task argv (`args.go:101`). Its input comes from:
- **Headless dispatch start:** `consult/consult.go:402-404`. The env is `cfg.DispatchLaunchEnv` (`:398`), args are built at `:521`, and the run id exists from `:458`.
- **Headless continuation:** `consult/continuation.go:82-83`, with args at `:94` and `:145`.
- **Interactive launch:** `consult/interactive_runtime.go:149-150`. The env is `cfg.DispatchEnv` (`:144`), args are built at `:175`, and `req.ID` is already set.
- **Consult kind:** same `Start` path. `resolveClaudeDispatchProfile` returns early for any kind other than dispatch (`claude_profile.go:12`).
- **Agents:** `agent/args.go:88-93`, reached through `BuildTemplateArgs` (`manager.go:754`, `:1088`, `:1837`).
  - Under `mcp: none` they get the empty fallback, with no bridge and no `mcp_config`.
  - Env is composed after args are built (`manager.go:775`), and restarts replay the stored argv (`manager.go:1702-1706`).
- **Oneshot tasks:** `run/runner.go:1125-1134`, same pattern as agents. Persistent tasks presumably use the agent path (not verified).

## Interfaces: `internal/harness/claude/mcp_servers.go` (new)
```go
type MCPServerDefs map[string]json.RawMessage // name → definition, verbatim

// Looks names up in top-level mcpServers of StateFile(env) ($CLAUDE_CONFIG_DIR/.claude.json,
// else $HOME/.claude.json — NOT session.ConfigDir). Unknown name, missing/unreadable/malformed
// file, reserved "leo" → warnings, never errors. Warnings carry names + path only.
func ResolveMCPServers(env map[string]string, names []string) (MCPServerDefs, []string)
func resolveMCPServers(path string, names []string, readFile func(string) ([]byte, error)) (MCPServerDefs, []string)

// MCP=="none": StrictMCPConfig = base servers (bridge, if present) ∪ defs, bridge wins on a name
// clash; sets StrictMCPConfigPrivate when any def was added. MCP=="inherit" + names → unchanged
// + one warning. MCP=="" → unchanged, silent (D5). Returns a copy.
func ApplyMCPServers(o Options, env map[string]string) (Options, []string)

func MCPConfigSpillPath(homePath, name string) string        // <home>/state/mcp/<name>.json; "" if name unusable (SettingsSpillPath rules)
func WriteMCPConfigFile(path string, data []byte) error      // 0600, dir 0700, temp+rename; refuses ""
func MaterializeStrictMCPConfig(o Options, path string) (Options, error) // private → write + set StrictMCPConfigFile; else no-op
```
- **Options:** `MCPServers []string` is decoded. `StrictMCPConfigPrivate bool` and `StrictMCPConfigFile string` are set at runtime and stay zero from `DecodeOptions`.
- **`strictMCPArgs`** changes to return `([]string, error)`:
  - When `File` is set, it emits `--strict-mcp-config --mcp-config <file>`.
  - When the config is private and `File` is empty, it returns an error. It fails closed and never inlines the config.
  - Otherwise it behaves exactly as today.

## Steps

**1. Decode, schema, golden**
- Failing tests:
  - `options_test.go` `TestDecodeOptionsMCPServers`: a list decodes in order. A non-list, a non-string element, or `""` gives an error. Duplicates are removed (D8).
  - `TestOptionsSchemaMatchesDecodeOptions` (`:133`).
  - `TestConfigAPIGolden` (`internal/web/config_api_golden_test.go:60`).
- Change:
  - Add `mcp_servers` to `optionKeys`, to `DecodeOptions` (via `stringSliceOption`), and to `OptionsSchema` as `OptionStringList` with help text "with mcp: none, also load these user-scope servers from the account's .claude.json".
  - Regenerate with `env -u TMUX go test ./internal/web -run TestConfigAPIGolden -update`. The diff should only add the new field.
  - The web form needs no changes (`harnessform.go:67/159`).
  - Optional: one test row each showing codex (`codex/options.go:133`) and opencode (`opencode/options.go:55`) reject the key.

**2. Resolver**
- Failing tests in `mcp_servers_test.go` (`testenv` `TestMain` already isolates HOME):
  - Found names come back byte-equal, and other servers are left out.
  - A missing name is skipped and the warning names it and the path.
  - A missing file, a malformed file, `mcpServers` that isn't an object, or an entry that isn't an object each produce a warning and no defs. A sentinel secret from a def's headers must appear in no warning.
  - `leo` is reserved.
  - `{CLAUDE_CONFIG_DIR: tmp}` reads `tmp/.claude.json`. `{HOME: h, CLAUDE_CONFIG_DIR: ""}` reads `h/.claude.json` (acceptance 4).

**3. Merge, apply, writer, argv**
- Failing tests:
  - `ApplyMCPServers` cases:

    | Input | Expected |
    |---|---|
    | `none`, bridge, `[chrome]` | config is exactly {leo, chrome}, `Private` set |
    | `none`, no bridge, `[chrome]` | config is {chrome} |
    | `none`, every name missing | config byte-identical, `Private` false, warnings |
    | `inherit` | unchanged, one warning |
    | no names | unchanged, no warnings |
    | any | input not mutated |

  - `WriteMCPConfigFile`: file perm 0600, dir 0700, overwrite works, `""` is an error.
  - `MCPConfigSpillPath`: rejects bad names.
  - `MaterializeStrictMCPConfig`: when not private, no file is created.
  - `args_test.go`:
    - With `File` set, argv holds the path and no element contains `mcpServers`.
    - Private without `File` gives an `Args` error.
    - `TestTaskArgsDispatchProfile` (`:261`) and `TestTaskArgsMCPNoneExcludesOtherMCPConfigs` (`:273`) stay unchanged (acceptance 3).
- Change:
  - Implement the functions above.
  - `WriteMCPConfigFile` does its own `""` check first, then reuses `writePrivateFile` (`settings.go:196`).
  - Update the callers at `args.go:50` and `:101` for the new error return.

**4. Headless dispatch and continuation**
- Add a consult helper `resolveClaudeDispatchOptions(...) (claudeharness.Options, []string)`. It calls the profile, then `ApplyMCPServers` for every kind.
- Add `dispatchMCPConfigPath(home, id) = MCPConfigSpillPath(home, dispatchSpillPrefix+id)`.
- Failing tests, using the injected-bin pattern from `claude_profile_test.go:80`. Setup: template env sets `CLAUDE_CONFIG_DIR=tmp`, and `.claude.json` holds `chrome` (with a secret header) plus another server.
  - argv contains exactly `--mcp-config <home>/state/mcp/dispatch-<id>.json`. No element contains the secret or `mcpServers`.
  - The file is 0600 and holds exactly {leo, chrome} while the run is live. It is gone after completion.
  - An unknown name still launches, and `Started.Warnings` and `Record.Warnings` hold the warning.
  - A template with only `mcp_servers` (no `mcp` key) still gets the lean `none`.
  - `inherit` keeps today's argv and adds the warning.
  - Continuation (`continuation_test.go:15` pattern) rewrites the file and removes it when done.
- Change:
  - `consult.go:402-404` calls the new helper.
  - Warnings go into the `Record` literal (`:457`).
  - After `recorder.Open`, write `fmt.Fprintf(os.Stderr, "dispatch %s (template %s): warning: %s\n", …)`.
  - Before `:513`, call `MaterializeStrictMCPConfig`. If it fails, the dispatch fails (D6).
  - Store the path in `runState.mcpConfigFile` (under `d.mu`), and clean it up on the error returns at `:521` and `:528`.
  - `lifecycle.go` `complete` (`:70`) removes `runState.mcpConfigFile`.
  - `continuation.go:82-85` uses the same helper, then materializes before `:94`. Its warnings go to stderr only.

**5. Interactive launch, release, sweep**
- Failing tests:
  - Extend `TestInteractiveClaudeDispatchLaunchProfile` (`interactive_runtime_test.go:501`): the launch command carries only the path, the file is 0600 with {leo, chrome}, and the template's `CLAUDE_CONFIG_DIR` is the one read.
    - Also confirm that the env from `cfg.DispatchEnv` at `:144` is the env the pane actually launches with. If it isn't, resolve from the launch env instead.
  - `spill_cleanup_test.go`: `ReleaseRunFiles` removes `state/mcp/dispatch-<id>.json`.
  - Extend `TestSweepRunFilesKeepsOnlyLiveRuns` (`:82`) and `TestDispatcherSweepRunFilesRemovesBriefFilesForTerminalAndUnknownRuns` (`:147`) with MCP files.
- Change:
  - `interactive_runtime.go:149-150` calls the helper. Its warnings go to stderr only (D4). Materialize before `h.Args` (`:175`).
  - `spill_cleanup.go`:
    - `ReleaseRunFiles` adds `removeIfExists(dispatchMCPConfigPath(home,id), "mcp config", id)`.
    - `SweepRunFiles` adds `sweepGlob(<home>/state/mcp/dispatch-*.json, dispatchSpillPrefix, ".json", "mcp config", keep)`.
    - Startup already calls it through `ReconcileRestart` (`restart_reconcile.go:194`).

**6. Surface warnings**
- Failing tests:
  - Both `Start` paths return `Started.Warnings`.
  - The `leo_dispatch` start text (`mcp/tools.go:571-583`) has one `warning: …` line per warning.
  - CLI start prints the warnings to stderr (`cli/dispatch.go:173`).
  - The `leo dispatch watch` header (`cli/consult.go:214-216`) prints the record's warnings.
- Change:
  - Add `Warnings []string json:"warnings,omitempty"` to `Started` (`request.go:85`) and to `Record` (`record.go:146`, copied in `cloneRecord`).
  - Confirm `web/handlers_consult.go:124` passes `Started` through intact.
  - The JSON change is additive only.

**7. Agents and tasks: explicit no-op**
- Failing tests:
  - `agent/args_test.go`: `mcp: none` plus `mcp_servers` gives argv identical to the same config without `mcp_servers`, and logs `[agent:<n>] mcp_servers applies to dispatches only; ignored`.
  - Same check in `run/runner_test.go`, with a `[task:<n>]` prefix.
- Change: one log line at `agent/args.go:89` and one at `runner.go:1126`, only when `MCP=="none"` and the list is non-empty.

**8. Docs**
- `docs/configuration/harnesses.md`:
  - Line 168: change "nine" to "ten".
  - `mcp` row (178): cross-reference `mcp_servers`.
  - New row after 179: with `mcp: none`, add the named servers from the run's account config to a private 0600 file under `state/mcp/`, passed by path. Unknown names are skipped with a warning; the list is ignored with a warning under `inherit`; dispatches only.
  - Add a `chrome` example.
- No test needed.

## Verification
- **Per step:**
  - Steps 1–3: `env -u TMUX go test -race ./internal/harness/claude/`
  - Steps 4–6: `env -u TMUX go test -race ./internal/consult/`
  - Steps 1 and 6: `env -u TMUX go test -race ./internal/web/ ./internal/mcp/ ./internal/cli/`
  - Step 7: `env -u TMUX go test -race ./internal/agent/ ./internal/run/`
  - Codex and opencode packages, if those test rows were added.
- **Gate:** `make lint`, then `env -u TMUX go test -race ./...` in the background (under `heavy` if it runs past ~2 min). New tests must not use sleeps.
- **e2e:** never from a dispatch pane. The orchestrator runs it at the merge gate as `env -i HOME=$HOME PATH=$PATH TMUX_TMPDIR=$(mktemp -d) make e2e`, because argv changes.
- **Live check** (isolated test daemon only; never restart production). Use a template with `mcp_servers: [chrome, nope]` and confirm:
  - `ps -o args` shows the path only;
  - `stat -f %Lp` gives `600`;
  - the file holds leo and chrome only;
  - the start output shows the `nope` warning;
  - the file is gone after `leo_release`;
  - a planted `state/mcp/dispatch-dead.json` is removed by the startup sweep.
- **Review:** `review.security`, covering secrets on disk, in argv and in logs. Also grep for `%+v` of `spec`/`Options` in consult and agent code.

## Open decisions
- **D1 Agents and tasks scope.** They don't go through the dispatch code path, so I recommend dispatch and consult only, plus the Step 7 warning. Appendix B covers full support if you want it.
- **D2 Reserved name `leo`.** The bridge wins and a warning is logged.
- **D3 Where warnings appear.** New `Started.Warnings` and `Record.Warnings` fields, which leoterm and Den will see as additive fields, rather than reusing `Note`. Recommended.
- **D4 Launch-time re-resolution.** Log only. Callers see the warnings from `Start`.
- **D5 Inherit warning.** It fires only for an explicit `mcp: inherit`. An unset `mcp` (consults, agents) stays silent, so a `defaults`-level list doesn't log on every spawn or consult.
- **D6 File write failure.** It fails the dispatch, closed. It never falls back to inline.
- **D7 Unreadable or malformed account config.** A warning, with every name skipped. Not an error.
- **D8 Names in the list.** Duplicates are removed silently; `""` is a decode error.

## Appendix B: full agent and task support (only if D1 says so)
- **Agents:**
  - Resolve after `composeEnv` (`manager.go:775`), because `CLAUDE_CONFIG_DIR` comes from the named, literal and spawn layers. Then rebuild or patch the args at all three `BuildTemplateArgs` call sites (`:754`, `:1088`, `:1837`).
  - The file is `state/mcp/<agent>.json` and lives as long as the agent, because stored-argv replay refers to it.
  - Remove it next to `removeSettingsSpill` (`manager.go:2050`, `:2288`). Sweep it next to `SweepSettingsSpills` (`service/process.go:1111`), skipping the `dispatch-` prefix.
  - Under agent `none` there is no bridge, so the file holds the named servers only.
- **Tasks:**
  - Resolve from `spawnEnv` (`runner.go:448`), after checking its order relative to `buildArgs`.
  - The file is `state/mcp/task-<run-id>.json`, removed by `defer`. The startup sweep removes all `task-*` files; first confirm oneshot children die with the daemon.
  - Confirm persistent tasks use the agent path.

## Orchestrator decisions (2026-11-14)
D1–D8 accepted as recommended: dispatches and consults only (agents and tasks log the Step 7 "dispatches only" warning, no Appendix B); additive Started/Record.Warnings; the file-write failure fails the dispatch closed.
