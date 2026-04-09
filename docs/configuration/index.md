# Configuration

Leo is configured via a single `leo.yaml` file in the Leo home directory (`~/.leo/`).

## Location

The config file lives at `~/.leo/leo.yaml`. Leo auto-detects it by walking up from the current working directory, falling back to `~/.leo/leo.yaml`. You can also specify it explicitly:

```bash
leo --config /path/to/leo.yaml <command>
```

## Example Configuration

```yaml
telegram:
  bot_token: "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11"
  chat_id: "123456789"
  group_id: "-100XXXXXXXXXX"          # optional: forum group

defaults:
  model: sonnet
  max_turns: 15
  remote_control: true

processes:
  telegram:
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
    topic_id: 3
    enabled: true
    silent: true
```

## Sections Overview

### `telegram`

Bot credentials and optional forum group/topic routing. The `bot_token` and `chat_id` are required for all messaging.

### `defaults`

Default model, max turns, and other settings applied to all processes and tasks unless overridden.

### `processes`

Named long-running Claude sessions. Each process can specify its own workspace, channels, model, and settings. Used for interactive services like Telegram.

### `tasks`

Named tasks with cron schedules, prompt files, and optional overrides. Each task can override the default model and max turns, and can use its own workspace.

---

See [Config Reference](config-reference.md) for the full field-by-field specification.
