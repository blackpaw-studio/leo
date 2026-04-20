# Config Reference

Complete field-by-field reference for `leo.yaml`.

Config lives at `~/.leo/leo.yaml` (the Leo home directory).

## `defaults`

Settings inherited by all processes, tasks, and templates unless overridden.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `model` | string | No | Default Claude model (`sonnet`, `opus`, `haiku`). Defaults to `sonnet`. |
| `max_turns` | int | No | Default maximum agent turns per execution. Defaults to `15`. |
| `permission_mode` | string | No | Default permission mode (`default`, `acceptEdits`, `auto`, `bypassPermissions`, `dontAsk`, `plan`). |
| `bypass_permissions` | bool | No | Legacy: pass `--dangerously-skip-permissions`. Prefer `permission_mode`. Default `false`. |
| `remote_control` | bool | No | Enable `--remote-control` for web/mobile access. Default `false`. |
| `allowed_tools` | list | No | Default tool whitelist (passed via `--allowed-tools`). |
| `disallowed_tools` | list | No | Default tool blacklist (passed via `--disallowed-tools`). |
| `append_system_prompt` | string | No | Extra system prompt appended to all processes/tasks. |

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

Click **Sign out** in the top-right of the dashboard to destroy the session.

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
    - 10.0.4.16      # the IP your LAN will use to reach this host
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
      ssh: evan@leo.example.com
      ssh_args: ["-p", "2222"]
      leo_path: /usr/local/bin/leo
      tmux_path: /opt/homebrew/bin/tmux
    dev:
      ssh: evan@devbox.local
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
    remote_control: true
    enabled: true
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `workspace` | string | No | `~/.leo/workspace/` | Working directory for this process. |
| `channels` | list | No | -- | Channel plugin IDs (e.g., `plugin:telegram@claude-plugins-official`). |
| `dev_channels` | list | No | -- | Unpublished channel plugin IDs loaded via `--dangerously-load-development-channels`. |
| `model` | string | No | `defaults.model` | Claude model override. |
| `max_turns` | int | No | `defaults.max_turns` | Max turns override. |
| `agent` | string | No | -- | Run as a specific agent definition. |
| `permission_mode` | string | No | `defaults.permission_mode` | Permission mode override. |
| `bypass_permissions` | bool | No | `defaults.bypass_permissions` | Legacy: skip permissions. |
| `remote_control` | bool | No | `defaults.remote_control` | Enable remote control. |
| `mcp_config` | string | No | `<workspace>/config/mcp-servers.json` | Path to MCP config file. |
| `add_dirs` | list | No | -- | Additional directories passed via `--add-dir`. |
| `env` | map | No | -- | Environment variables for the claude process. |
| `allowed_tools` | list | No | `defaults.allowed_tools` | Tool whitelist. |
| `disallowed_tools` | list | No | `defaults.disallowed_tools` | Tool blacklist. |
| `append_system_prompt` | string | No | `defaults.append_system_prompt` | Extra system prompt. |
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
| `model` | string | No | `defaults.model` | Claude model override. |
| `max_turns` | int | No | `defaults.max_turns` | Max turns override. |
| `timeout` | string | No | `30m` | Max duration before kill (e.g., `30m`, `1h`). |
| `retries` | int | No | `0` | Retry attempts on failure. |
| `channels` | list | No | -- | Channel plugin IDs used by `notify_on_fail`. |
| `dev_channels` | list | No | -- | Unpublished channel plugin IDs loaded via `--dangerously-load-development-channels`. |
| `permission_mode` | string | No | `defaults.permission_mode` | Permission mode override. |
| `allowed_tools` | list | No | `defaults.allowed_tools` | Tool whitelist. |
| `disallowed_tools` | list | No | `defaults.disallowed_tools` | Tool blacklist. |
| `append_system_prompt` | string | No | `defaults.append_system_prompt` | Extra system prompt. |
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
    remote_control: true
    permission_mode: auto
    workspace: ~/agents
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `workspace` | string | No | `~/.leo/agents/` | Base directory for agent workspaces. Repos are cloned as subdirectories. |
| `channels` | list | No | -- | Channel plugin IDs for spawned agents. |
| `dev_channels` | list | No | -- | Unpublished channel plugin IDs loaded via `--dangerously-load-development-channels`. |
| `model` | string | No | `defaults.model` | Claude model. |
| `max_turns` | int | No | `defaults.max_turns` | Max turns. |
| `agent` | string | No | -- | Agent definition to use. |
| `remote_control` | bool | No | `true` | Enable remote control (defaults to on for templates). |
| `mcp_config` | string | No | -- | Path to MCP config file. |
| `add_dirs` | list | No | -- | Additional directories. |
| `env` | map | No | -- | Environment variables. |
| `permission_mode` | string | No | `defaults.permission_mode` | Permission mode. |
| `allowed_tools` | list | No | `defaults.allowed_tools` | Tool whitelist. |
| `disallowed_tools` | list | No | `defaults.disallowed_tools` | Tool blacklist. |
| `append_system_prompt` | string | No | `defaults.append_system_prompt` | Extra system prompt. |

When dispatching with a repo (`/agent coding owner/repo` via a channel plugin, or `leo agent spawn coding --repo owner/repo`), Leo clones the repo into `<workspace>/<repo>` using `gh`. The agent session is named `leo-<template>-<owner>-<repo>`.

## Override Cascade

Process, task, and template settings override defaults:

```
effective value = process/task/template value OR defaults value
```
