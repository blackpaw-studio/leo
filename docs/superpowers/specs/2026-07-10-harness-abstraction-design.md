# Harness Abstraction — Design

**Date:** 2026-07-10
**Status:** Approved (design), pending implementation plan

## Summary

Remove the `providers` feature (third-party Anthropic-compatible endpoints) and replace it with a first-class **harness abstraction**: leo drives multiple coding agent CLIs — Claude Code, OpenAI Codex, and Opencode — behind a single Go interface. All three leo primitives (supervised processes, scheduled tasks, ephemeral agents) work with any harness. The Go interface is the plugin contract: third parties add support for a new coding agent by contributing a compiled-in adapter via PR. No runtime plugin loading.

## Goals

- Full parity: processes, tasks, and ephemeral agents all accept a `harness` setting.
- Ship adapters for `claude`, `codex`, `opencode`.
- Delete the `providers` feature entirely.
- Consolidate the three duplicated argv builders into one neutral `LaunchSpec` pipeline.
- Leo's MCP server is the harness-neutral bridge for messaging and control (all three harnesses speak MCP).

## Non-Goals

- Runtime-loadable plugins (manifests or executable protocols). Adapters are Go code, compiled in.
- Channel plugins on non-Claude harnesses. Channels remain Claude Code plugins; a non-Claude agent messages via leo's MCP tools instead.
- Porting Claude-specific features (remote control, `--agent`, startup-dialog dismissal) to other harnesses.
- Backward-compatible config. Leo is a solo-user project; the config migration is a documented one-time break.

## Decisions (with rationale)

1. **Go interface, adapters compiled in.** Go cannot load code at runtime; declarative manifests can't express harness *behavior* (session semantics, output parsing, driving model). The interface is the extension point; "plugin" means "PR an adapter."
2. **Per-harness session drivers, not a generalized tmux glyph probe.** Neither Codex nor Opencode documents a TUI readiness contract, and both offer better programmatic paths: Codex is turn-per-process (`codex exec resume <id>`), Opencode has a first-class HTTP server (`opencode serve` + `run --attach`). Screen-scraping stays a Claude-only implementation detail behind the driver interface.
3. **`harness_options` block for harness-specific config.** Common fields (model, workspace, env, channels, mcp, schedule, timeout, retries) stay flat; harness-specific knobs move under an opaque `harness_options` map validated by the adapter. Claude-only fields (`permission_mode`, `allowed_tools`, `disallowed_tools`, `append_system_prompt`, `remote_control`, `agent`, `bypass_permissions`) move into the claude adapter's options.
4. **Providers deleted, not abstracted.** Custom endpoints are the harness's own concern (Opencode natively supports arbitrary providers; Claude users can set `ANTHROPIC_BASE_URL` via per-process `env`).
5. **`channels` on a non-Claude harness is a validation error.** Channel plugins cannot load outside Claude Code. Fail loudly rather than silently dropping delivery.

## Architecture

### New package: `internal/harness`

```go
type Harness interface {
    Name() string

    // One-shot runs (tasks): neutral LaunchSpec → executable command.
    OneShotCmd(spec LaunchSpec) (Cmd, error)

    // Parse the harness's output stream → result text, session ID, error flag.
    ParseEvents(r io.Reader) (Result, error)

    // Session persistence: resume/new-session args, latest-session lookup, staleness.
    Sessions() SessionStore

    // How leo keeps a live session and injects messages into it.
    Driver() SessionDriver

    // Config integration.
    ValidateModel(m string) error
    OptionsSchema() schema.Object // feeds web UI forms + config validation
    MCPArgs(configPath string) ([]string, []EnvVar)
}
```

Registry: `harness.Get(name)` returns the adapter; `harness.Names()` for validation and web UI dropdowns. Compiled-in: `claude`, `codex`, `opencode`.

### LaunchSpec

One neutral struct replacing the three copy-paste builders (`buildProcessArgs` in `internal/cli/service.go`, `BuildTemplateArgs` in `internal/agent/args.go`, task `buildArgs` in `internal/run/runner.go`):

```go
type LaunchSpec struct {
    Workspace      string
    Prompt         string            // one-shot only
    Model          string
    Session        SessionState      // new | resume(id) | fresh-with-id
    MCPConfigPath  string            // leo's MCP server config, when web enabled
    Env            map[string]string
    Channels       []string          // claude adapter only; others reject
    HarnessOptions map[string]any    // opaque; validated via OptionsSchema
}
```

The claude adapter is a **pure refactor**: its argv output for every existing config shape is byte-identical to today's builders, locked by golden tests.

### Session drivers

```go
type SessionDriver interface {
    // Start (or arrange) a live session for a supervised process/agent.
    Start(ctx context.Context, spec LaunchSpec, sess SessionHandle) error
    // Inject a message into the live session.
    Inject(ctx context.Context, sess SessionHandle, msg string) error
    // What `leo agent attach` does for this harness.
    Attach(sess SessionHandle) AttachSpec
}
```

- **claude → TmuxTUIDriver.** Today's machinery moves behind the interface: the `❯ ` readiness glyph, the echo probe (`internal/tmux/inject.go`), and startup-dialog auto-dismissal (`internal/service/process.go`) become claude-adapter data/callbacks rather than package-level constants. Behavior unchanged.
- **codex → TurnDriver.** No persistent process. Each injected message spawns `codex exec resume <thread_id> --json` in the workspace; the first turn is `codex exec --json` and the driver records the `thread_id` from the `thread.started` event. "Restart" is a no-op; supervision reduces to bookkeeping. Attach surfaces turn history (e.g. tail of the transcript log).
- **opencode → ServerDriver.** The supervised long-running process is `opencode serve --port <p>`; injection is `opencode run --attach http://127.0.0.1:<p> -s <session-id> "<msg>"`; `leo agent attach` maps to `opencode attach <url>`.

### Session stores

- **claude:** existing `internal/session/slug.go` logic (reads `~/.claude/projects/<slug>/*.jsonl` for latest session and mtime staleness) becomes the claude `SessionStore`. The `--session-id → --resume → fresh` degradation ladder stays claude-specific.
- **codex:** sessions live at `~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<id>.jsonl`; resume via `codex exec resume <id>`.
- **opencode:** session IDs come from the event stream (`ses_…` on every event); listing via `opencode session list --format json` or the server API.

### MCP bridge

Every harness gets leo's MCP server (when web is enabled), making `leo_send_message` etc. available regardless of harness:

- **claude:** `--mcp-config <leo-mcp.json>` (today's `internal/leomcp` path).
- **codex:** per-invocation `-c mcp_servers.leo.command=…` TOML dot-notation overrides — no config-file mutation.
- **opencode:** `OPENCODE_CONFIG_CONTENT` env var with an inline `mcp` block — per-spawn, no file mutation.

## Config

```yaml
defaults:
  harness: claude            # cascades: defaults → process/template/task/session
  model: opus

processes:
  builder:
    harness: opencode
    model: anthropic/claude-sonnet-5
    harness_options:
      permission:
        bash: allow

tasks:
  nightly:
    harness: codex
    model: gpt-5.3-codex
    harness_options:
      sandbox: workspace-write
      approval: never
  digest:                    # claude, from defaults
    channels: ["plugin:telegram@claude-plugins-official"]
    harness_options:
      permission_mode: acceptEdits
      append_system_prompt: "…"
```

### Validation rules

- `harness` must name a registered adapter.
- `model` validation delegates to the adapter: claude keeps its hardcoded list (sonnet/opus/haiku + [1m] variants); codex and opencode do format checks only (opencode requires `provider/model` shape).
- `harness_options` validated against the adapter's `OptionsSchema()`.
- Claude-only fields at the old flat locations produce a **precise migration error** ("`permission_mode` moved to `harness_options.permission_mode`"), not silent acceptance or silent dropping.
- `channels` set on a non-claude harness → validation error.
- `providers:` section present → validation error pointing at the removal notes.

### Removals

- `internal/provider/` and `internal/config/provider.go`
- `providers:` config section, its validation, and the two env-injection points (`internal/cli/service.go`, `internal/service/process.go` shell exports)
- `docs/configuration/providers.md` (replaced by `docs/configuration/harnesses.md`)

## Known constraints (baked into adapters)

- **Codex has no append-system-prompt.** Only workspace `AGENTS.md` or full replacement via `model_instructions_file`. Since `append_system_prompt` is claude-scoped now, this is a documentation note, not a mapping problem.
- **Opencode `run --format json` may exit without flushing the final `step_finish` event** (known upstream bug). Its `ParseEvents` treats stream EOF as turn end and accumulates `text` events, never hard-depending on `step_finish`.
- **Codex `CODEX_API_KEY`** is only honored by `codex exec` — fine, that's the only mode leo uses headlessly.
- **Prereq checks** (`internal/prereq`) become harness-aware: check each binary the loaded config actually references, not just `claude`.

## What stays Claude-only

Channel plugins, `remote_control`, `--agent` files, startup-dialog dismissal, the `--session-id` crash-loop degradation ladder, and the stale-resume jsonl mtime check (other harnesses get simpler equivalents or none).

## Web UI

- Harness dropdown (from `harness.Names()`) on process/template/task/defaults forms.
- `OptionsSchema()` plugs into the existing schema-driven form system (`internal/web/schema`) so each harness's options render as real form fields, not a raw YAML textarea.
- Providers page removed.

## Error handling

- Unknown harness name → config validation error listing registered names.
- Harness binary missing → prereq error at daemon boot / task start, named per harness.
- Adapter option validation failures → field-level errors surfaced in CLI validation and web forms.
- Opencode server unreachable during inject → surfaced as delivery failure in task history, same path as today's injection failures.

## Testing

- **Golden argv tests per adapter.** Claude's assert byte-identical output to the current three builders across representative config shapes (this is the refactor's safety net).
- **ParseEvents fixtures** captured from real `codex exec --json` and `opencode run --format json` streams, including the opencode missing-`step_finish` case.
- **Config tests:** migration errors for old flat claude fields, channels-on-non-claude rejection, providers-section rejection, harness cascade resolution.
- **Driver tests** using the existing testability seams (`execCommand`, `supervisedExecFn` pattern) — no live binaries required.
- **Gated e2e smoke:** run a trivial one-shot task per harness when its binary is present on the machine; skipped otherwise.

## Rollout

Implementation lands as reviewable PRs in dependency order (interface + claude pure-refactor first, then providers removal, then codex/opencode adapters, then drivers/parity, then web UI). Exact sequencing belongs to the implementation plan, not this spec.
