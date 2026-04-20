# CLI Reference

Leo provides commands for setup, process management, task scheduling, template management, agent dispatch, and day-to-day operations.

## Command Overview

### Setup & lifecycle

| Command | Description |
|---------|-------------|
| [`leo setup`](setup.md) | Interactive setup wizard |
| [`leo update`](update.md) | Self-update the binary (verifies cosign signature) |
| [`leo validate`](validate.md) | Check config, prerequisites, and workspace health |
| [`leo completion`](completion.md) | Generate shell completion script (bash/zsh/fish) |
| [`leo version`](version.md) | Print version |

### Processes, agents, and tasks

| Command | Description |
|---------|-------------|
| [`leo service`](service.md) | Manage persistent Claude sessions and the daemon |
| [`leo process`](process.md) | List, add, remove, enable, or disable supervised processes |
| [`leo task`](task.md) | Manage scheduled tasks (list, add, remove, enable, disable, history, logs) |
| [`leo template`](template.md) | Inspect and remove agent templates |
| [`leo agent`](agent.md) | Spawn and control ephemeral agents (local or via SSH) |
| [`leo attach`](attach.md) | Attach to a supervised process or running agent (with optional interactive picker) |
| [`leo run`](run.md) | Run a scheduled task once |

### Observability

| Command | Description |
|---------|-------------|
| [`leo status`](status.md) | Overall status (service, processes, tasks, templates, web UI) |
| [`leo logs`](logs.md) | Tail service or per-process logs |
| [`leo session list`](session.md#leo-session-list) | List stored session mappings |
| [`leo session clear`](session.md#leo-session-clear) | Clear stored session(s) |

### Config

| Command | Description |
|---------|-------------|
| [`leo config show`](config.md#leo-config-show) | Display effective config with defaults applied |
| [`leo config edit`](config.md#leo-config-edit) | Edit `leo.yaml` interactively |
| [`leo config path`](config.md#leo-config-path) | Print the resolved `leo.yaml` path |

### Integrations

| Command | Description |
|---------|-------------|
| [`leo web`](web.md) | Web UI utilities (`login-url`) |
| [`leo channels`](channels.md) | Per-channel-type bootstrap (e.g. register slash commands with Telegram) |
| [`leo mcp-server`](mcp-server.md) | MCP server wired into supervised processes (internal) |

### Deprecated

| Command | Description |
|---------|-------------|
| [`leo onboard`](onboard.md) | Legacy onboarding flow — superseded by `leo setup` |
| [`leo cron`](cron.md) | Legacy cron entrypoint — superseded by `leo task` |

## Global Flags

```
-c, --config <path>       Path to leo.yaml
```

### Config Auto-Detection

If `--config` is not specified, Leo walks up from the current working directory looking for a `leo.yaml` file. If none is found, it falls back to `~/.leo/leo.yaml`. Use [`leo config path`](config.md#leo-config-path) to see which file the resolution landed on.
