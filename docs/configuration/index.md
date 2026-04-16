# Configuration

Leo is configured via a single `leo.yaml` file in the Leo home directory (`~/.leo/`).

## Location

The config file lives at `~/.leo/leo.yaml`. Leo auto-detects it by walking up from the current working directory, falling back to `~/.leo/leo.yaml`. You can also specify it explicitly:

```bash
leo --config /path/to/leo.yaml <command>
```

## Example Configuration

```yaml
defaults:
  model: sonnet
  max_turns: 15
  remote_control: true

processes:
  assistant:
    channels:
      - plugin:telegram@claude-plugins-official
    enabled: true

tasks:
  daily-news-briefing:
    schedule: "0 7 * * *"
    timezone: America/New_York
    prompt_file: reports/daily-news-briefing.md
    model: opus
    max_turns: 20
    channels:
      - plugin:telegram@claude-plugins-official
    notify_on_fail: true
    enabled: true
    silent: true
```

## Sections Overview

### `defaults`

Default model, max turns, and other settings applied to all processes and tasks unless overridden.

### `processes`

Named long-running Claude sessions. Each process can specify its own workspace, channels, model, and settings.

### `tasks`

Named tasks with cron schedules, prompt files, and optional overrides. Each task can override the default model and max turns, specify its own channels for `notify_on_fail`, and use its own workspace.

### Channels

Channels are Claude Code plugin IDs (e.g., `plugin:telegram@claude-plugins-official`). Install the plugin via `claude plugin install <id>` and reference it in a process or task `channels:` list. Leo passes the list to the spawned Claude process via `LEO_CHANNELS`; the plugin owns its own credentials and routing.

For plugins not yet published to a registry, use `dev_channels:` instead. Leo passes them via `--dangerously-load-development-channels` and auto-accepts the in-terminal confirmation prompt for supervised processes. See the [Config Reference](config-reference.md#development-channels) for details.

---

See [Config Reference](config-reference.md) for the full field-by-field specification.
