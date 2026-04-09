# CLI Reference

Leo provides commands for setting up your assistant, running tasks, managing cron entries, and managing the service.

## Command Overview

| Command | Description |
|---------|-------------|
| [`leo setup`](setup.md) | Interactive setup wizard |
| [`leo onboard`](onboard.md) | Guided first-time setup with prerequisite checks |
| [`leo service`](service.md) | Manage persistent Claude sessions |
| [`leo run <task>`](run.md) | Run a scheduled task once |
| [`leo cron`](cron.md) | Manage cron entries |
| [`leo task`](task.md) | Manage scheduled tasks |
| [`leo validate`](validate.md) | Validate configuration |
| [`leo status`](status.md) | Show overall status |
| [`leo config show`](config.md) | Display current configuration |
| [`leo version`](version.md) | Print version |

## Global Flags

These flags are available on all commands:

```
-c, --config <path>       Path to leo.yaml
```

### Config Auto-Detection

If `--config` is not specified, Leo walks up from the current working directory looking for a `leo.yaml` file. If none is found, it falls back to `~/.leo/leo.yaml`.
