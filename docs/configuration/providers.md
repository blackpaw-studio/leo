# Providers — Third-Party Models

Leo can run any process, template, session, or task against a third-party
model exposed through an Anthropic-Messages-compatible endpoint (z.ai GLM,
OpenRouter, Moonshot, DeepSeek, MiniMax, …). Leo keeps using the stock
`claude` CLI — it just points it at a different backend via environment
variables at spawn time.

## Defining providers

```yaml
providers:
  glm:
    base_url: https://api.z.ai/api/coding/paas/v4
    api_key_env: GLM_API_KEY          # name of an env var holding the key
    default_model: glm-5.2
  openrouter:
    base_url: https://openrouter.ai/api
    api_key_cmd: op read "op://Vault/OpenRouter/api-key"   # stdout = key
```

| Field | Required | Meaning |
|---|---|---|
| `base_url` | yes | Anthropic-compatible endpoint; injected as `ANTHROPIC_BASE_URL` |
| `api_key_env` | one of | Env var holding the API key |
| `api_key_cmd` | one of | Shell command whose trimmed stdout is the key |
| `default_model` | no | Model used when the scope doesn't set `model:` |

Exactly one of `api_key_env` / `api_key_cmd` is required. Keys never live in
`leo.yaml` and are never written to Leo's state files.

## Selecting a provider

`provider:` is available on `defaults`, `processes.*`, `templates.*`,
`sessions.*`, and `tasks.*`, and cascades from `defaults` like every other
setting. Unset means Anthropic, exactly as before.

```yaml
processes:
  scout:
    provider: glm
    model: glm-5.2      # any string is allowed once a provider is set
```

Model cascade with a provider: `model:` on the scope → the provider's
`default_model` → `defaults.model` → `sonnet`.

## Switching and failure behavior

Switching is manual: edit `provider:` and restart the process / respawn the
agent. There is no automatic rate-limit failover.

If a key can't be resolved at spawn time, interactive commands fail with an
error; at daemon boot the affected process/session/agent is skipped with a
warning and everything else starts normally.

`api_key_env` caveat: the daemon captures its environment when you run
`leo service start --daemon`. After exporting a new key var, run that command
again — or use `api_key_cmd` (e.g. `op read …`), which resolves fresh on
every spawn.

## What to expect from third-party models

Client-side features (channel plugins, skills, hooks, MCP, sessions) work
unchanged. Tool-call fidelity may degrade at long context on non-Claude
models, vision is unavailable on most third-party endpoints, and
Claude-specific API features depend on the backend.
