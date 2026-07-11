# Config Reference

Complete field-by-field reference for `leo.yaml`.

Config lives at `~/.leo/leo.yaml` (the Leo home directory).

## `defaults`

Settings inherited by all processes, tasks, and templates unless overridden.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `model` | string | No | Default model, validated by the resolved harness (`claude`: `sonnet`, `opus`, `haiku`, `sonnet[1m]`, `opus[1m]`; `codex`: any non-whitespace string; `opencode`: must be `provider/model`). Defaults to `sonnet`. Does **not** cascade to a scope whose resolved `harness` differs from `defaults.harness` — see [Harnesses → Cross-harness model cascade](harnesses.md#cross-harness-model-cascade). |
| `max_turns` | int | No | Default maximum agent turns per execution. Defaults to `15`. Ignored by `codex` and `opencode` (no per-turn cap upstream). |
| `harness` | string | No | Adapter name for this scope and everything that cascades from it. One of `claude`, `codex`, `opencode`. Defaults to `claude`. All three run every leo primitive (tasks, processes, ephemeral agents, persistent sessions) — see [Harnesses](harnesses.md). |
| `harness_options` | map | No | Adapter-specific options, strictly validated by the resolved harness. For `claude`: `permission_mode`, `bypass_permissions`, `remote_control`, `agent`, `allowed_tools`, `disallowed_tools`, `append_system_prompt`. For `codex`: `sandbox`. For `opencode`: `permission`. See [Harnesses](harnesses.md) for the full reference and merge rules. |
| `idle_suspend_after` | string | No | Idle interval (Go duration, e.g. `24h`) after which an ephemeral agent is suspended. Empty/unset disables it. See [Idle-suspend](#idle-suspend). |

Custom Anthropic-compatible endpoints (z.ai GLM, OpenRouter, Moonshot, DeepSeek, MiniMax, …) are configured via each scope's own `env:` map (`ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`) — see [Harnesses → providers is gone](harnesses.md#providers-is-gone).

### `harness_options` by harness

Every scope's `harness_options` map is strictly validated by that scope's
resolved `harness:` — unknown keys, wrong types, and invalid enum values are
all rejected at config load and before every web-UI save. Full behavior
(merge rules, resume, MCP bridge, auth, etc.) is in [Harnesses](harnesses.md).

**`claude`** (7 keys):

| Key | Type | Meaning |
|---|---|---|
| `permission_mode` | string | `acceptEdits`, `auto`, `bypassPermissions`, `default`, `dontAsk`, `plan`. |
| `bypass_permissions` | bool | Legacy `--dangerously-skip-permissions` fallback; only consulted when `permission_mode` is empty. |
| `remote_control` | bool | Enables `--remote-control`. |
| `agent` | string | Path to a subagent file, passed via `--agent`. |
| `allowed_tools` | list of strings | Tool whitelist. |
| `disallowed_tools` | list of strings | Tool blacklist. |
| `append_system_prompt` | string | Extra text appended to the system prompt. |

**`codex`** (1 key):

| Key | Type | Meaning |
|---|---|---|
| `sandbox` | string | `read-only` (codex default), `workspace-write`, or `danger-full-access`. |

**`opencode`** (1 key):

| Key | Type | Meaning |
|---|---|---|
| `permission` | map | Per-tool `allow`/`ask`/`deny`, or a nested pattern map of the same. Delivered via a per-spawn `OPENCODE_CONFIG_CONTENT` overlay, not argv. |

## `web`

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `enabled` | bool | No | `false` | Enable the web dashboard. |
| `port` | int | No | `8370` | TCP port for the web UI. |
| `bind` | string | No | `127.0.0.1` | Bind address. Loopback-only by default. |
| `allowed_hosts` | list of strings | No | `[]` | Extra hostnames/IPs accepted in the `Host` and `Origin` headers, in addition to loopback. Required when `bind` is non-loopback. Entries must not include a port. |

When enabled, the daemon serves a web dashboard with process monitoring, task management, agent dispatch, config editing, and cron preview.

### Authentication

Both browser and API access require the same token. On first start the daemon mints a random 64-hex-char token and writes it to `~/.leo/state/api.token` (mode 0600).

**Browser login.** Visit the dashboard and you'll be redirected to `/login`. Paste the token there and a 7-day session cookie is set (HttpOnly, SameSite=Strict). For convenience:

```bash
leo web login-url
```

prints a one-click URL (`http://<bind>:<port>/login?token=...`) — the login page auto-submits if the token is in the query string. The URL contains the token; don't share it.

Click **Sign out** at the bottom of the sidebar to destroy the session.

**API access.** `/api/*` endpoints take the token in an `Authorization: Bearer` header:

```bash
curl -H "Authorization: Bearer $(cat ~/.leo/state/api.token)" \
  http://127.0.0.1:8370/api/status
```

Rotate the token by deleting `api.token` and restarting the daemon. Existing browser sessions remain valid until they expire (7 days).

**Token scope.** The bearer token grants access to the full daemon API — including `/web/*` routes that can restart the service, mutate config, send keys to supervised processes, and write prompt files. Treat it like a root credential. Supervised Claude processes receive this token via `LEO_API_TOKEN` so the built-in MCP server can call `/api/*`; if you don't trust a channel plugin with full daemon access, don't install it as a supervised process.

### Non-loopback access

`bind` defaults to `127.0.0.1`. To expose the web UI on your LAN:

```yaml
web:
  enabled: true
  bind: 0.0.0.0
  port: 8370
  allowed_hosts:
    - 192.0.2.10      # the IP your LAN will use to reach this host
    - leo.local      # or a hostname
```

`allowed_hosts` entries are checked against the incoming `Host` and `Origin` headers to defend against DNS-rebinding and drive-by cross-origin POSTs. Entries must be bare hostnames or IPs — no port, no scheme. `allowed_hosts` is required when `bind` is non-loopback; `leo validate` will fail otherwise.

The daemon prints a startup warning when `bind` is non-loopback.

## `client`

Remote-host definitions used by the `leo agent` CLI when `leo` is invoked as a client of a different machine. Empty on server configs.

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `default_host` | string | No | — | Host name to use when `--host` and `LEO_HOST` are unset. |
| `hosts` | map | No | `{}` | Named host definitions keyed by short name. |

Each entry under `hosts` has:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `ssh` | string | Yes | SSH target passed verbatim (e.g. `user@host`, or a `Host` alias from `~/.ssh/config`). |
| `ssh_args` | list | No | Extra arguments inserted between the target and the remote command (e.g. `["-p", "2222"]`). |
| `leo_path` | string | No | Absolute path to `leo` on the remote host. Defaults to `$HOME/.local/bin/leo` (matches `install.sh`). Override when `leo` is installed elsewhere or the remote's non-interactive SSH shell doesn't have it on PATH. |
| `tmux_path` | string | No | Path to `tmux` on the remote host. Used by `agent attach` and `agent logs --follow`. Defaults to `tmux` (relies on PATH). Set to `/opt/homebrew/bin/tmux` for macOS arm64 homebrew remotes, `/usr/local/bin/tmux` for macOS intel. |

```yaml
client:
  default_host: prod
  hosts:
    prod:
      ssh: alice@leo.example.com
      ssh_args: ["-p", "2222"]
      leo_path: /usr/local/bin/leo
      tmux_path: /opt/homebrew/bin/tmux
    dev:
      ssh: alice@devbox.local
```

Why `leo_path` exists: SSH runs a non-interactive shell on the remote, which doesn't source `.zshrc` / `.bashrc`. If `leo` lives in `~/.local/bin` and PATH is only extended in `.zshrc`, bare `leo` won't resolve. The default full path avoids that; set `leo_path` explicitly when the remote installs leo elsewhere (Homebrew, `/usr/local/bin`, etc.).

Resolution order for the target host: `--host` flag → `LEO_HOST` env → `default_host` → first entry sorted by key → localhost (only when no hosts are configured). `--host localhost` is a hard override. See the [Remote CLI guide](../guides/remote-cli.md).

## Channels

Leo does not ship with any built-in messaging channel. Channels are Claude Code plugins the user installs separately (e.g. Telegram, Slack, webhook). In `leo.yaml` they are referenced by plugin ID strings like `plugin:telegram@claude-plugins-official` on the `channels:` field of processes and tasks.

Leo passes the resolved list to the spawned Claude process via the `LEO_CHANNELS` environment variable. The plugin owns its own credentials, routing, and inbound-message handling.

To install a channel plugin:

```bash
claude plugin install telegram@claude-plugins-official
```

Then reference it:

```yaml
processes:
  assistant:
    channels: [plugin:telegram@claude-plugins-official]
```

## Development Channels

For channel plugins that aren't yet published to a registry (or for local plugin development), processes, tasks, and templates accept a parallel `dev_channels:` field. Leo passes each entry to Claude Code via `--dangerously-load-development-channels <id>` and exports the list in `LEO_DEV_CHANNELS`.

```yaml
processes:
  assistant:
    channels: [plugin:blackpaw-telegram@blackpaw-plugins]
    dev_channels: [plugin:blackpaw-telegram@blackpaw-plugins]
```

Validation matches `channels` — each entry must be a valid plugin ID.

Claude Code displays a confirmation prompt before loading development channels. For supervised processes, Leo watches the tmux pane and auto-accepts the prompt so the session starts non-interactively. Silent/nonexistent entries are ignored by Claude Code without warning — verify spellings carefully.

## `processes`

Each process is a named entry under the `processes` map. Processes define long-running Claude sessions supervised by the daemon.

```yaml
processes:
  assistant:
    channels: [plugin:telegram@claude-plugins-official]
    harness_options:
      remote_control: true
    enabled: true
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `workspace` | string | No | `~/.leo/workspace/` | Working directory for this process. |
| `channels` | list | No | -- | Channel plugin IDs (e.g., `plugin:telegram@claude-plugins-official`). Only valid on a channel-supporting harness. |
| `dev_channels` | list | No | -- | Unpublished channel plugin IDs loaded via `--dangerously-load-development-channels`. |
| `model` | string | No | `defaults.model` | Model override, validated by the resolved harness. |
| `harness` | string | No | `defaults.harness` | Adapter override for this process. All three harnesses support supervised processes. See [Harnesses](harnesses.md). |
| `harness_options` | map | No | merged with `defaults.harness_options` (same harness only) | Adapter-specific options — for `claude`: `permission_mode`, `bypass_permissions`, `remote_control`, `agent`, `allowed_tools`, `disallowed_tools`, `append_system_prompt`. See [Harnesses](harnesses.md). |
| `max_turns` | int | No | `defaults.max_turns` | Max turns override. |
| `mcp_config` | string | No | `<workspace>/config/mcp-servers.json` | Path to MCP config file. |
| `add_dirs` | list | No | -- | Additional directories passed via `--add-dir`. |
| `env` | map | No | -- | Environment variables for the claude process. |
| `enabled` | bool | No | `false` | Whether the service should start this process. |

## `tasks`

Each task is a named entry under the `tasks` map. Tasks are invoked by the in-process cron scheduler or manually via `leo run <task>`.

```yaml
tasks:
  daily-briefing:
    schedule: "0 7 * * *"
    timezone: America/New_York
    prompt_file: prompts/daily-briefing.md
    enabled: true
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `workspace` | string | No | `~/.leo/workspace/` | Working directory. |
| `schedule` | string | Yes | -- | 5-field cron expression. |
| `timezone` | string | No | System default | IANA timezone (e.g., `America/New_York`). |
| `prompt_file` | string | Yes | -- | Path to prompt file, relative to workspace. |
| `model` | string | No | `defaults.model` | Model override, validated by the resolved harness. Does not fall back to `defaults.model` when this task's harness differs from `defaults.harness` — see [Cross-harness model cascade](harnesses.md#cross-harness-model-cascade). |
| `harness` | string | No | `defaults.harness` | Adapter override for this task. `claude`, `codex`, and `opencode` all support one-shot and `runtime: persistent` tasks (persistent tasks run through sessions, which all three harnesses support). See [Harnesses](harnesses.md). |
| `harness_options` | map | No | merged with `defaults.harness_options` (same harness only) | Adapter-specific options — for `claude`: `permission_mode`, `bypass_permissions`, `allowed_tools`, `disallowed_tools`, `append_system_prompt`. `bypass_permissions` at task scope is honored (not defaults-only). For `codex`: `sandbox`. For `opencode`: `permission`. See [Harnesses](harnesses.md). |
| `max_turns` | int | No | `defaults.max_turns` | Max turns override. Ignored by `codex`/`opencode`. |
| `timeout` | string | No | `30m` | Max duration before kill (e.g., `30m`, `1h`). |
| `retries` | int | No | `0` | Retry attempts on failure. |
| `channels` | list | No | -- | Channel plugin IDs used by `notify_on_fail`. Only valid on a channel-supporting harness. |
| `dev_channels` | list | No | -- | Unpublished channel plugin IDs loaded via `--dangerously-load-development-channels`. |
| `notify_on_fail` | bool | No | `false` | Spawn a short child `claude` invocation on non-zero exit, instructing it to notify the configured channels. Requires `channels:` to be set. |
| `enabled` | bool | No | `false` | Whether the scheduler should run this task. |
| `silent` | bool | No | `false` | Prepend silent-mode preamble to prompt. |

### Silent Mode

When `silent: true`, Leo prepends a preamble instructing the agent to work without narration. The agent should deliver its final message via a configured channel plugin or output `NO_REPLY` if there's nothing to report.

## `templates`

Templates are reusable blueprints for spawning ephemeral agents. Dispatch them via the HTTP API, a channel plugin that exposes agent commands, or the web UI.

```yaml
templates:
  coding:
    model: sonnet
    workspace: ~/agents
    harness_options:
      remote_control: true
      permission_mode: auto
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `workspace` | string | No | `~/.leo/agents/` | Base directory for agent workspaces. Repos are cloned as subdirectories. |
| `channels` | list | No | -- | Channel plugin IDs for spawned agents. Only valid on a channel-supporting harness. |
| `dev_channels` | list | No | -- | Unpublished channel plugin IDs loaded via `--dangerously-load-development-channels`. |
| `model` | string | No | `defaults.model` | Model, validated by the resolved harness. |
| `harness` | string | No | `defaults.harness` | Adapter override for this template. All three harnesses support ephemeral agents. See [Harnesses](harnesses.md). |
| `harness_options` | map | No | merged with `defaults.harness_options` (same harness only), **except `remote_control`** | Adapter-specific options — for `claude`: `permission_mode`, `bypass_permissions`, `remote_control`, `agent`, `allowed_tools`, `disallowed_tools`, `append_system_prompt`. `remote_control` is template-own-only (no inheritance from `defaults.harness_options.remote_control`) and defaults to `true`. See [Harnesses](harnesses.md). |
| `max_turns` | int | No | `defaults.max_turns` | Max turns. |
| `mcp_config` | string | No | -- | Path to MCP config file. |
| `add_dirs` | list | No | -- | Additional directories. |
| `env` | map | No | -- | Environment variables. |
| `idle_suspend_after` | string | No | `defaults.idle_suspend_after` | Idle interval (Go duration) before agents from this template are suspended. Empty inherits the default. |

When dispatching with a repo (`/agent coding owner/repo` via a channel plugin, or `leo agent spawn coding --repo owner/repo`), Leo clones the repo into `<workspace>/<repo>` using `gh`. The agent session is named `leo-<template>-<owner>-<repo>`.

## Idle-suspend

Ephemeral agents can be **suspended** after a period of inactivity to free local
resources (the claude process and tmux session are killed) while preserving the
workspace and conversation. Off by default — enable it by setting an interval:

```yaml
defaults:
  idle_suspend_after: "24h"      # global default

templates:
  reviewer:
    idle_suspend_after: "30m"    # per-template override
```

Or per spawn: `leo agent spawn reviewer owner/repo --idle-suspend 24h`.

The cascade is **spawn flag → template → defaults**; the resolved interval is
stamped onto the agent at spawn time. Behavior:

- **Activity** is measured by the agent's tmux `session_activity` — injected
  prompts, interactive typing in an attached pane, and the agent's own output
  all count.
- An agent with a **client attached** is never suspended, even past the
  interval (so reading scrollback won't yank the session out from under you).
- A suspended agent shows as `suspended` in `leo agent list`. It **auto-resumes**
  on the next incoming message (e.g. `leo_send_message`), rejoining its prior
  conversation via `--resume`. You can also resume or suspend manually:

  ```bash
  leo agent suspend <name>
  leo agent resume <name>
  ```

- Suspended agents stay suspended across daemon restarts (they are not
  resurrected at boot), and their worktrees are never pruned.

## State directory

Leo's runtime state lives under `~/.leo/state/` (or `<home>/state` for a
non-default leo home). Beyond `sessions.json`, `history.json`, and
`api.token` (see [Authentication](#authentication)), non-`claude` session
drivers persist their own per-tmux-session records here:

| Path | Written by | Contents |
|---|---|---|
| `state/opencode/<tmux-session>.json` | `opencode` `ServerDriver` | The provisioned `opencode serve` port, basic-auth password, and last-used model for one supervised process/agent/session (mode `0600`). Reused across restarts so the port and password stay stable. |
| `state/transcripts/<tmux-session>.log` | `codex` `TurnDriver` | Append-only per-turn transcript (`user`/`codex` entry pairs, timestamped) for one process/agent/session, since codex has no resident process or live pane to attach to. `leo attach`/`leo agent attach`/`leo session attach` tail this file for codex sessions. |

See [Harnesses → Session driver semantics](harnesses.md#session-driver-semantics)
for how each driver uses these files.

## Override Cascade

Process, task, and template settings override defaults:

```
effective value = process/task/template value OR defaults value
```
