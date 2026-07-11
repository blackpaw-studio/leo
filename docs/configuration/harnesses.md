# Harnesses

A **harness** is the coding-agent CLI leo drives — `claude` today, with `codex`
and `opencode` planned. Every process, template, task, and session picks a
harness (directly or via cascade) and configures it through a strictly
validated `harness_options:` map instead of a flat, harness-specific field
list.

## What a harness is

Leo doesn't hardcode "Claude Code" into its config schema or argv builders
anymore. Instead, each supported CLI is an **adapter**: a small Go package
that knows how to validate its own options, translate a harness-neutral
launch spec into argv, and (for interactive harnesses) parse session/channel
state.

- `claude` is the only registered adapter today (`internal/harness/claude`).
- `codex` and `opencode` adapters are planned; they'll register the same way.
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

- An unregistered `harness:` name: `defaults.harness "foo" is not a registered harness (available: claude)`.
- An unknown `harness_options` key, wrong type, or invalid enum value:
  reported by the adapter's `DecodeOptions`, e.g. `defaults.harness_options: unknown option "foo" (valid: agent, allowed_tools, append_system_prompt, bypass_permissions, disallowed_tools, permission_mode, remote_control)`.
- `channels:`/`dev_channels:` on a harness that doesn't support them:
  `processes.foo.channels: the codex harness does not support channel plugins; use leo's MCP tools for messaging`.

If you hit any of these on an existing `leo.yaml`, move the named field
under `harness_options` (or drop `provider`/`providers` and switch to
`env:`) and re-run `leo validate`.
