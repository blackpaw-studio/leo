# Provider Config — Third-Party Models via Anthropic-Compatible Endpoints

**Date:** 2026-07-08
**Status:** Approved

## Problem

Leo can only run agents on Anthropic models through the stock `claude` CLI.
Evan wants to experiment with other models (GLM, GPT, etc.) — for hedging,
quality comparison, and as a manual escape hatch when Claude usage limits hit.

## Decision

Keep the `claude` CLI as Leo's only agent runtime. Route other models into it
via **Anthropic-Messages-compatible endpoints**, configured per
process/template/task and injected as environment variables at spawn time.

Explicitly rejected: native support for other agent CLIs (opencode, codex).
Leo's supervision machinery — tmux readiness probes, startup-dialog dismissal,
prompt injection, `~/.claude/projects/<slug>/*.jsonl` session resume, channel
plugins — is deeply claude-specific (an abstraction would span ~25+ files),
and channel plugins are Claude Code plugins that do not exist in other
harnesses. Every serious alternative-model vendor now ships an official
Anthropic-compatible endpoint marketed for Claude Code (z.ai GLM, OpenRouter
for GPT/everything, Moonshot, DeepSeek, MiniMax), and Anthropic documents the
`ANTHROPIC_BASE_URL` gateway path as a supported feature. If multi-CLI support
ever becomes worthwhile, target the Agent Client Protocol (ACP) — not per-CLI
shims.

## Config

### New top-level `providers` map

```yaml
providers:
  glm:
    base_url: https://api.z.ai/api/coding/paas/v4
    api_key_env: GLM_API_KEY
    default_model: glm-5.2
  openrouter:
    base_url: https://openrouter.ai/api
    api_key_cmd: op read "op://Olympus/OpenRouter/api-key"
```

Fields per provider:

| Field | Required | Meaning |
|---|---|---|
| `base_url` | yes | Anthropic-compatible endpoint; becomes `ANTHROPIC_BASE_URL` |
| `api_key_env` | one of | Name of an env var holding the key (resolved from the daemon's captured environment) |
| `api_key_cmd` | one of | Shell command whose stdout (trimmed) is the key — e.g. `op read …` |
| `default_model` | no | Model used when the process/task doesn't set one |

Exactly one of `api_key_env` / `api_key_cmd` must be set. Secrets never live
in `leo.yaml`.

### New `provider` field on defaults, processes, templates, sessions, and tasks

```yaml
processes:
  scout:
    provider: glm
    model: glm-5.2
```

Cascades like every other setting: task/process/template → `defaults` →
unset. Unset means Anthropic, exactly as today — existing configs are
unchanged in behavior.

## Env injection at spawn

When a resolved provider is set, Leo injects into the spawned claude process:

- `ANTHROPIC_BASE_URL` = provider `base_url`
- `ANTHROPIC_AUTH_TOKEN` = resolved key (`api_key_env` lookup or `api_key_cmd` output)
- `ANTHROPIC_MODEL` = resolved model, when it is not a claude alias
  (`sonnet`/`opus`/`haiku` variants)

Key resolution happens at spawn time, once per spawn. `api_key_cmd` failure
(non-zero exit or empty output) fails the spawn with a clear error — no silent
fallback to Anthropic.

Injection points (the three spawn paths; arg-builders are untouched):

1. **One-shot task runner** — `internal/run/runner.go` `executeCommand`
   (already sets `LEO_CHANNELS` etc.)
2. **Supervised tmux launch** — `internal/service/process.go`
   `buildClaudeShellCmd` env exports (covers persistent processes and
   ephemeral agents)
3. **Persistent task sessions** — `internal/service/session.go` env exports

The provider must be persisted alongside the process/agent spec so restarts
and `RestoreAgents` re-inject the same env.

## Validation (`Config.Validate()`)

- Provider entries: `base_url` must parse as an http(s) URL; exactly one of
  `api_key_env`/`api_key_cmd` present and non-empty.
- `provider` references (on defaults/processes/templates/sessions/tasks) must
  name an entry in `providers`.
- `model` validation relaxes when a provider is resolved for that scope: any
  non-empty string is accepted. With no provider, the existing
  `sonnet`/`opus`/`haiku` enum still applies.
- Web UI config save path inherits all of this (it already calls `Validate()`).

## Switching

Manual only: edit `provider:` on the process/task, then restart it
(`leo service restart <name>` / respawn the agent). No rate-limit detection,
no automatic failover.

Behavioral note: a persistent session resumed under a different provider
continues its existing transcript on the new model. This works — it is just a
mid-conversation model swap.

## Known limitations (accepted)

- Tool-call fidelity on non-Claude models degrades at long context
  (documented GLM streaming/parsing issues).
- Vision is unavailable on most third-party endpoints (z.ai is text-only).
- Claude-specific API features (web search tool, thinking betas, caching
  semantics) depend on the backend implementing them.
- No Anthropic support for gateway setups; the path is documented but
  Anthropic-controlled.

## Out of scope (YAGNI)

- Automatic rate-limit fallback (`fallback_provider` + retry-path change is
  the natural future extension — the `providers` map is the seam).
- claude-code-router or any local routing proxy.
- Web UI provider toggle.
- opencode/codex or any second agent-CLI backend.

## Testing

- Unit: provider validation (URL shape, key-source exclusivity, dangling
  `provider` refs, model-enum relaxation), cascade resolution
  (task/process/template → defaults), key resolution (`api_key_env` hit/miss,
  `api_key_cmd` success/failure/empty), env-injection output for each of the
  three spawn paths (via the existing `execCommand`/exec seams).
- Integration: one-shot task run with a fake provider (env var key) asserts
  the spawned command's environment contains the three `ANTHROPIC_*` vars;
  no-provider run asserts they are absent.
