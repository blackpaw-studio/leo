# Harnesses

A **harness** is the coding-agent CLI leo drives — `claude`, `codex`, and
`opencode` today. Every process, template, task, and session picks a harness
(directly or via cascade) and configures it through a strictly validated
`harness_options:` map instead of a flat, harness-specific field list.

All three harnesses run every leo primitive: scheduled tasks (`leo run
<task>` / cron), supervised processes, ephemeral agents, and persistent
sessions (`processes.*`, `templates.*`, `sessions.*`, and `tasks.*` with
`runtime: persistent`). The only thing that stays claude-only is channel
plugins — `codex` and `opencode` message via leo's own MCP tools instead (see
[Support matrix](#support-matrix) and [Session driver semantics](#session-driver-semantics)
below).

## Support matrix

| | `claude` | `codex` | `opencode` |
|---|---|---|---|
| Scheduled tasks (`leo run`) | ✅ | ✅ | ✅ |
| Supervised processes | ✅ | ✅ | ✅ |
| Ephemeral agents | ✅ | ✅ | ✅ |
| Persistent sessions | ✅ | ✅ | ✅ |
| Channel plugins (`channels:`/`dev_channels:`) | ✅ | ❌ (use `leo_send_message` MCP tool) | ❌ (use `leo_send_message` MCP tool) |

## Session driver semantics

Each harness drives a live session (supervised process, ephemeral agent, or
persistent session) with a different strategy, exposed internally as a
`harness.SessionDriver`:

- **`claude` — `TmuxTUIDriver`.** A resident `claude` TUI process lives inside
  the leo-managed tmux session for the process/agent/session's whole
  lifetime. Messages are injected via `tmux paste-buffer` + `send-keys
  Enter`, unchanged from pre-Plan-4 behavior. `leo attach`/`leo session
  attach` drop you straight into the live tmux pane.
- **`codex` — `TurnDriver`.** No resident process at all. Each injected
  message spawns a fresh `codex exec --json --skip-git-repo-check [--model
  …] [--sandbox …] [-c mcp_servers.leo.*…] [resume <thread-id>] <message>`,
  which blocks until the turn completes; the returned `thread_id` is stored
  and passed to `resume` on the next message. A "restart" of a codex
  process/agent/session is bookkeeping only — a no-op beyond recording that
  the session is alive, since there's no process to actually restart. A
  resume against a vanished thread (`codex` reports "no rollout found") is
  detected and retried once as a fresh turn, clearing the stale thread id
  first. There is **no live attach** for codex — `leo attach`/`leo agent
  attach` instead show the tail of a per-turn transcript recorded at
  `<home>/state/transcripts/<tmux-session>.log` (one `user`/`codex` entry
  pair appended per turn).
- **`opencode` — `ServerDriver`.** A resident `opencode serve --port <p>
  --hostname 127.0.0.1` process is supervised in tmux (crash-restarted, same
  port every time). Leo allocates the port (an ephemeral localhost bind-then-
  close) and a random 32-hex-char password the first time a session/process/
  agent starts, and persists both — plus the model in use — to
  `<home>/state/opencode/<tmux-session>.json` (0600), reused stably across
  restarts. Messages are injected via `opencode run --attach <url> --format
  json --dir <workspace> [--model …] [-s <session-id>] <message>`, which
  blocks until the turn completes (opencode's `--attach` event stream is
  lossy, so leo treats process exit — not a `step_finish` event — as the
  turn-end signal). `leo attach` maps to `opencode attach <url> --dir
  <workspace> -p <password> [-s <session-id>]` — opencode's own TUI client,
  talking to the resident server over `127.0.0.1` with the stored basic-auth
  password (passed as an argv flag, not env, since attach is
  interactive/user-invoked and env doesn't cross an SSH hop).

## What a harness is

Leo doesn't hardcode "Claude Code" into its config schema or argv builders
anymore. Instead, each supported CLI is an **adapter**: a small Go package
that knows how to validate its own options, translate a harness-neutral
launch spec into argv, and (for interactive harnesses) parse session/channel
state.

- Three adapters are registered today: `claude` (`internal/harness/claude`),
  `codex` (`internal/harness/codex`), and `opencode` (`internal/harness/opencode`).
- Adapters are **compiled-in Go, contributed by PR** — there is no runtime
  plugin mechanism for harnesses. (Don't confuse this with *channel*
  plugins — Telegram, Slack, etc. — which remain regular Claude Code plugins
  installed via `claude plugin install`.)

Adding a new harness means writing a new package under `internal/harness/`
and blank-importing it from `internal/config/harness.go`; it does not touch
any consumer's config shape beyond that adapter's own `harness_options` keys.

## Config shape

Every scope that used to accept flat claude fields now has two knobs:

- **`harness:`** — the adapter name for that scope. Optional; when unset on
  a process/template/task/session it cascades from `defaults.harness`, and
  `defaults.harness` itself falls back to the built-in default `claude` when
  unset. Applies to `defaults`, `processes.*`, `templates.*`, `tasks.*`, and
  `sessions.*` — there is no separate top-level `harness:` key; `defaults`
  is the root of the cascade.
- **`harness_options:`** — a map of adapter-specific keys. The adapter named
  by that scope's resolved `harness:` strictly validates the map at config
  load and on every web-UI save: unknown keys, wrong value types, and
  invalid enum values are all rejected with a precise error.

```yaml
defaults:
  harness: claude   # optional; this *is* the built-in default
  model: sonnet
  harness_options:
    permission_mode: default
    allowed_tools: [Read, Grep]

processes:
  assistant:
    workspace: ~/.leo/workspace
    channels: [plugin:telegram@claude-plugins-official]
    harness_options:
      permission_mode: acceptEdits
      remote_control: true
      agent: leo
      allowed_tools: [Read, Edit, Bash]
      disallowed_tools: [WebFetch]
    enabled: true

templates:
  coding:
    workspace: ~/agents
    harness_options:
      permission_mode: auto
      remote_control: true    # template-own-only; see below

tasks:
  daily-briefing:
    schedule: "0 7 * * *"
    prompt_file: prompts/daily-briefing.md
    harness_options:
      permission_mode: bypassPermissions
    enabled: true
```

### Merge rules

- **`harness:` cascade**: scope value → `defaults.harness` → `claude`. A
  scope that names a harness must name a *registered* adapter, or config
  validation fails.
- **`harness_options:` merge**: `defaults.harness_options` is layered
  *underneath* a scope's own `harness_options`, one top-level key at a time —
  the scope's value for a given key always wins; it does not deep-merge
  nested values. This only happens when the scope resolves to the **same
  harness** as `defaults`; options never leak across harnesses (e.g. a
  `defaults.harness: codex` block's options are not merged into a
  `processes.foo` entry that sets `harness: claude`).
- **Sessions never inherit `defaults.harness_options`.** This matches
  pre-migration behavior — persistent sessions never cascaded the flat
  claude fields from `defaults` either — and the migration preserves it
  exactly. Set every option you want directly under `sessions.<name>.harness_options`.
- **Template `remote_control` is template-own-only.** Unlike every other
  claude option, `templates.*.harness_options.remote_control` does *not*
  inherit from `defaults.harness_options.remote_control` — the defaults
  layer is ignored for this one key on templates. It defaults to `true` when
  unset (ephemeral agents are remote-controllable out of the box).

## Claude option reference

These are the seven `harness_options` keys the `claude` adapter accepts.
Unknown keys are rejected.

| Key | Type | Meaning |
|---|---|---|
| `permission_mode` | string | One of `acceptEdits`, `auto`, `bypassPermissions`, `default`, `dontAsk`, `plan`. Passed as `--permission-mode`. |
| `bypass_permissions` | bool | Legacy fallback for `--dangerously-skip-permissions`. **Only consulted when `permission_mode` is empty** — set `permission_mode` instead where possible. Task-level `bypass_permissions` is now honored (previously only `defaults` respected it). |
| `remote_control` | bool | Enables `--remote-control` (claude.ai / mobile app attach). |
| `agent` | string | Path to a Claude Code subagent file, passed via `--agent`. |
| `allowed_tools` | list of strings | Tool whitelist, passed via `--allowed-tools`. |
| `disallowed_tools` | list of strings | Tool blacklist, passed via `--disallowed-tools`. |
| `append_system_prompt` | string | Extra text appended to the system prompt. |

Model validation is also delegated to the adapter: for `claude`, `model:`
must be one of `sonnet`, `opus`, `haiku`, `sonnet[1m]`, `opus[1m]` (empty is
always valid — it means "let claude choose").

`channels:` / `dev_channels:` remain top-level fields (not `harness_options`)
but are only valid on a **channel-supporting** harness. `claude` is the only
one today (`SupportsChannels() == true`); setting `channels:` on a scope
whose resolved harness doesn't support channels is a validation error.

## Codex option reference

`codex` runs every leo primitive — scheduled tasks, supervised processes,
ephemeral agents, and persistent sessions — via `TurnDriver`, described in
[Session driver semantics](#session-driver-semantics) above.

| Key | Type | Meaning |
|---|---|---|
| `sandbox` | string | One of `read-only` (codex's own default when unset), `workspace-write`, `danger-full-access`. Passed as `--sandbox`. |

Other things to know:

- **No `approval:` key.** Headless `codex exec` has no approval flag at all —
  upstream removed it, and approval policy is hardcoded to `never`. Setting
  `harness_options.approval` is rejected: `option "approval" is not
  supported: codex exec always runs non-interactively (approval policy
  "never")`.
- **No `append_system_prompt`.** Codex's equivalent mechanism is a workspace
  `AGENTS.md` file, not a CLI flag. Setting the key is rejected: `option
  "append_system_prompt" is not supported: codex has no append-system-prompt
  equivalent (use the workspace AGENTS.md)`.
- **Model is free-form.** `codex.ValidateModel` only rejects whitespace; the
  actual model name is validated server-side by codex (an invalid one fails
  the run with a `model_not_found` error, not a leo validation error). An
  unset `model:` **does not** inherit `defaults.model` across harnesses — see
  [Cross-harness model cascade](#cross-harness-model-cascade) below.
- **`max_turns` is ignored.** Codex has no per-turn cap leo can drive; the
  value validates but has no effect.
- **Auth** is `CODEX_API_KEY` set in the task's `env:`, or ambient `codex
  login` state on the host running leo. Leo does not manage codex
  credentials.
- **`--skip-git-repo-check` is always passed.** Leo workspaces are frequently
  not git repos, and codex refuses to run in one otherwise.
- **Resume** happens via codex's own thread mechanism
  (`codex exec resume <thread-id>` under the hood) — leo persists the
  `thread_id` it reads from the `thread.started` event and passes it back on
  the next invocation for the same task.
- **MCP bridge.** When the web UI is enabled **and** leo can read a non-empty
  `state/api.token` file, leo injects its own MCP server into each invocation
  via per-invocation `-c mcp_servers.leo.*` overrides — no config-file
  mutation. (Web-enabled-but-no-token is not an error; leo silently skips
  wiring the bridge in, since the leo MCP server would be doomed to
  authenticate anyway.) This includes
  `mcp_servers.leo.default_tools_approval_mode="approve"`, scoped to just the
  leo server: without it, headless codex auto-cancels every MCP tool call
  (`user cancelled MCP tool call`) even though the turn still completes with
  exit 0.
- **Messaging** goes through leo's `leo_send_message` MCP tool — codex has no
  channel-plugin concept (`SupportsChannels() == false`).

## Opencode option reference

`opencode` runs every leo primitive — scheduled tasks, supervised processes,
ephemeral agents, and persistent sessions — via `ServerDriver`, described in
[Session driver semantics](#session-driver-semantics) above.

| Key | Type | Meaning |
|---|---|---|
| `permission` | map | Per-tool permission: `allow`, `ask`, or `deny`, or a nested pattern map of the same (e.g. `{bash: {"git push *": ask}}`). Delivered via a per-spawn config overlay — see below. |

Other things to know:

- **Delivery mechanism.** `permission` (and leo's MCP bridge entry) are not
  passed as argv — opencode's permission system is config-only. Leo builds a
  JSON overlay and sets it as the `OPENCODE_CONFIG_CONTENT` environment
  variable for that one spawn. Opencode deep-merges this over the user's own
  `opencode.json`/global config; leo never mutates that file.
- **Model must be `provider/model`.** `opencode.ValidateModel` rejects
  anything without a `/`, e.g. `anthropic/claude-sonnet-4-5`. An unset
  `model:` does not inherit `defaults.model` across harnesses — see
  [Cross-harness model cascade](#cross-harness-model-cascade) below.
- **`max_turns` is ignored**, same as codex.
- **No `append_system_prompt`.** Rejected: `option "append_system_prompt" is
  not supported: opencode has no append-system-prompt equivalent (use
  AGENTS.md or the instructions config)`.
- **Resume** uses opencode's own session IDs (`-s ses_…`), read from the
  `sessionID` field present on every event in the stream.
- **MCP bridge** rides in the same `OPENCODE_CONFIG_CONTENT` overlay, under a
  `mcp.leo` entry, gated the same way as codex's — web UI enabled **and** a
  readable, non-empty `state/api.token`.
- **Parsing quirks leo works around:**
  - *EOF-as-turn-end.* Older opencode versions (and `--attach` mode) can omit
    the terminal `step_finish` event on a truncated stream (upstream
    [#26855](https://github.com/sst/opencode/issues/26855)); leo treats
    reaching EOF as the end of a turn rather than requiring `step_finish`.
  - *In-stream errors fail the attempt even on exit 0.* Opencode sometimes
    exits `0` after emitting an `error` event mid-stream. Leo parses the
    stream and treats any `error` event as a failed attempt regardless of
    the process exit code — this is a deliberate cross-harness behavior
    change (claude exits non-zero on real errors, so this path is
    effectively unreachable there).
- **Messaging** goes through `leo_send_message`, same as codex —
  `SupportsChannels() == false`.

## Cross-harness model cascade

`defaults.model` **does not** cascade to a task whose resolved harness
differs from `defaults.harness` — model identifiers are harness-specific
(`opus` means nothing to codex; codex/opencode need their own model strings),
so leaking a claude default into a codex or opencode task would silently pass
garbage. Concretely: if `tasks.t.harness: codex` (or `opencode`) and
`tasks.t.model` is unset, `TaskModel` returns `""` regardless of what
`defaults.model` is set to — an empty model means "let the harness pick its
own default." The fall-through to `defaults.model` (then the built-in
default) only applies when the task's resolved harness matches
`defaults.harness`.

## Example: codex and opencode tasks

```yaml
defaults:
  harness: claude
  model: sonnet

tasks:
  codex-refactor:
    schedule: "0 3 * * *"
    prompt_file: prompts/codex-refactor.md
    harness: codex
    model: gpt-5.3-codex
    harness_options:
      sandbox: workspace-write
    env:
      CODEX_API_KEY: ${CODEX_API_KEY}
    enabled: true

  opencode-triage:
    schedule: "0 9 * * *"
    prompt_file: prompts/opencode-triage.md
    harness: opencode
    model: anthropic/claude-sonnet-4-5
    harness_options:
      permission:
        bash: ask
        edit: allow
    enabled: true
```

Note there is no `approval:` key for codex — approval policy is fixed at
`never` for headless exec and isn't configurable.

## Migration table

Every flat claude field that used to live directly on `defaults`,
`processes.*`, `templates.*`, `tasks.*`, or `sessions.*` has moved one level
down, under `harness_options`, with the same key name:

| Old field | New field |
|---|---|
| `permission_mode` | `harness_options.permission_mode` |
| `bypass_permissions` | `harness_options.bypass_permissions` |
| `remote_control` | `harness_options.remote_control` |
| `agent` | `harness_options.agent` |
| `allowed_tools` | `harness_options.allowed_tools` |
| `disallowed_tools` | `harness_options.disallowed_tools` |
| `append_system_prompt` | `harness_options.append_system_prompt` |

For example:

```yaml
# before
processes:
  assistant:
    permission_mode: acceptEdits
    remote_control: true

# after
processes:
  assistant:
    harness_options:
      permission_mode: acceptEdits
      remote_control: true
```

### `providers:` is gone

The entire `providers:` top-level section and every scope's `provider:`
field have been removed — there is no replacement config key. Leo no longer
brokers named third-party endpoints; it delegates that entirely to the
harness's own environment.

For claude, point a scope at a third-party Anthropic-Messages-compatible
endpoint (z.ai GLM, OpenRouter, Moonshot, DeepSeek, MiniMax, …) using its
existing per-scope `env:` map:

```yaml
processes:
  scout:
    env:
      ANTHROPIC_BASE_URL: https://api.z.ai/api/coding/paas/v4
      ANTHROPIC_AUTH_TOKEN: ${GLM_API_KEY}
```

`env:` works the same way on `tasks:` — both oneshot tasks and dedicated
persistent tasks (`runtime: persistent` with no `session:`) can target a
custom endpoint:

```yaml
tasks:
  nightly-report:
    schedule: "0 6 * * *"
    prompt_file: nightly-report.md
    env:
      ANTHROPIC_BASE_URL: https://api.z.ai/api/coding/paas/v4
      ANTHROPIC_AUTH_TOKEN: ${GLM_API_KEY}
```

This is the same mechanism `providers:` used internally to inject
`ANTHROPIC_BASE_URL`/`ANTHROPIC_AUTH_TOKEN` — it just skips the indirection
of a named provider table, and there's no more `api_key_cmd`/`default_model`
convenience layer; resolve secrets into `env:` values yourself (e.g. with a
wrapper script or your shell's env before `leo` reads its own environment).

**Model validation still applies.** `model:` is validated by the resolved
harness regardless of `env:` overrides — for `claude` that means only
`sonnet`, `opus`, `haiku`, `sonnet[1m]`, or `opus[1m]` pass validation
(or leave `model:` unset). If your third-party endpoint expects a different
model identifier, leave `model:` unset in `leo.yaml` and have the proxy on
the other end of `ANTHROPIC_BASE_URL` remap the request, or set the desired
model via an endpoint-specific env var your proxy reads instead of `--model`.
This is a real behavior change from `providers:`, which allowed an arbitrary
`default_model` string once a provider was set.

## Validation behavior

Every one of these mistakes is caught at config load (`leo validate`, CLI
startup, daemon boot) and before every web-UI config save. Each error names
the exact scope, the exact field, and points back here:

```
processes.foo.permission_mode has moved to processes.foo.harness_options.permission_mode (claude harness) — see docs/configuration/harnesses.md
providers: this section has been removed — see docs/configuration/harnesses.md
defaults.provider has been removed along with providers — see docs/configuration/harnesses.md
```

Other harness-related validation errors follow the same style:

- An unregistered `harness:` name: `defaults.harness "foo" is not a registered harness (available: claude, codex, opencode)`.
- An unknown `harness_options` key, wrong type, or invalid enum value:
  reported by the adapter's `DecodeOptions`, e.g. `defaults.harness_options: unknown option "foo" (valid: agent, allowed_tools, append_system_prompt, bypass_permissions, disallowed_tools, permission_mode, remote_control)`.
- `channels:`/`dev_channels:` on a harness that doesn't support them:
  `processes.foo.channels: the codex harness does not support channel plugins; use leo's MCP tools for messaging`.
- A kind the harness can't run: the literal error text baked into
  `internal/config/config.go` is `processes.foo.harness: the <name> harness
  cannot run supervised processes yet (only scheduled tasks) — see
  docs/configuration/harnesses.md` (same pattern for `templates.*.harness` →
  "run ephemeral agents", `sessions.*.harness` → "run persistent sessions",
  and a `runtime: persistent` task's `.harness` → "run persistent tasks yet
  (persistent tasks run through sessions)"). This is dead code for every
  built-in harness today — `claude`, `codex`, and `opencode` all pass
  `SupportsKind` for every primitive (see [Support matrix](#support-matrix))
  — it only fires for a future harness whose adapter doesn't implement
  `SupportsKind` for a given kind.

If you hit any of these on an existing `leo.yaml`, move the named field
under `harness_options` (or drop `provider`/`providers` and switch to
`env:`) and re-run `leo validate`.
