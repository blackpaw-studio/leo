# CLI Reference

Leo provides commands for setting up agents, running tasks, managing cron entries, and chatting over Telegram.

## Command Overview

| Command | Description |
|---------|-------------|
| [`leo setup`](setup.md) | Interactive setup wizard for a new agent |
| [`leo onboard`](onboard.md) | Guided first-time setup with prerequisite checks |
| [`leo chat`](chat.md) | Start an interactive Telegram session |
| [`leo run <task>`](run.md) | Run a scheduled task once |
| [`leo cron`](cron.md) | Manage cron entries |
| [`leo task`](task.md) | Manage scheduled tasks |
| [`leo migrate`](migrate.md) | Migrate from OpenClaw |
| [`leo version`](version.md) | Print version |

## Global Flags

These flags are available on all commands:

```
-c, --config <path>       Path to leo.yaml
-w, --workspace <path>    Workspace directory
```

### Config Auto-Detection

If `--config` is not specified, Leo walks up from the current working directory looking for a `leo.yaml` file. This means you can run Leo commands from anywhere inside your workspace without specifying the config path.

If `--workspace` is not specified, Leo reads the workspace path from the config file's `agent.workspace` field.
