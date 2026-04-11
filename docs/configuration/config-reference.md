# Config Reference

Complete field-by-field reference for `leo.yaml`.

Config lives at `~/.leo/leo.yaml` (the Leo home directory).

## `telegram`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `bot_token` | string | Yes | Telegram Bot API token from [@BotFather](https://t.me/BotFather). |
| `chat_id` | string | Yes | Your personal Telegram chat ID. Auto-detected during setup. |
| `group_id` | string | No | Forum group chat ID (starts with `-100`). Enables topic routing. |

### Topic Routing

Tasks can route output to specific forum topics in a Telegram group by setting `topic_id` to the numeric thread ID. Use `leo telegram topics` to discover available topic IDs for your group.

If `group_id` is set, messages go to the group. The `topic_id` field adds a `message_thread_id` to route to a specific thread. If no `topic_id` is specified, messages go to the General thread.

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
| `bind` | string | No | `0.0.0.0` | Bind address. |

When enabled, the daemon serves a web dashboard with process monitoring, task management, agent dispatch, config editing, and cron preview.

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
| `channels` | list | No | -- | Channel plugins (e.g., `plugin:telegram@claude-plugins-official`). |
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
| `topic_id` | int | No | -- | Telegram forum topic ID. |
| `permission_mode` | string | No | `defaults.permission_mode` | Permission mode override. |
| `allowed_tools` | list | No | `defaults.allowed_tools` | Tool whitelist. |
| `disallowed_tools` | list | No | `defaults.disallowed_tools` | Tool blacklist. |
| `append_system_prompt` | string | No | `defaults.append_system_prompt` | Extra system prompt. |
| `notify_on_fail` | bool | No | `false` | Send Telegram message on non-zero exit. |
| `enabled` | bool | No | `false` | Whether the scheduler should run this task. |
| `silent` | bool | No | `false` | Prepend silent-mode preamble to prompt. |

### Silent Mode

When `silent: true`, Leo prepends a preamble instructing the agent to work without narration. The agent should either send a Telegram message with results or output `NO_REPLY` if there's nothing to report.

## `templates`

Templates are reusable blueprints for spawning ephemeral agents. Dispatch them from Telegram (`/agent <template> <repo>`) or the web UI.

```yaml
templates:
  coding:
    model: sonnet
    remote_control: true
    permission_mode: bypassPermissions
    workspace: ~/agents
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `workspace` | string | No | `~/.leo/agents/` | Base directory for agent workspaces. Repos are cloned as subdirectories. |
| `channels` | list | No | -- | Channel plugins for spawned agents. |
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

When dispatching with a repo (`/agent coding owner/repo`), Leo clones the repo into `<workspace>/<repo>` using `gh`. The agent session is named `leo-<template>-<owner>-<repo>`.

## Override Cascade

Process, task, and template settings override defaults:

```
effective value = process/task/template value OR defaults value
```
