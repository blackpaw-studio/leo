# Configuration

Leo is configured via a single `leo.yaml` file in your workspace directory.

## Location

The config file lives at `<workspace>/leo.yaml`. By default, Leo auto-detects it by walking up from the current working directory. You can also specify it explicitly:

```bash
leo --config /path/to/leo.yaml <command>
leo --workspace /path/to/workspace <command>
```

## Example Configuration

```yaml
agent:
  workspace: ~/leo

telegram:
  bot_token: "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11"
  chat_id: "123456789"
  group_id: "-100XXXXXXXXXX"          # optional: forum group

defaults:
  model: sonnet
  max_turns: 15
  remote_control: true

tasks:
  heartbeat:
    schedule: "0,30 7-22 * * *"
    timezone: America/New_York
    prompt_file: HEARTBEAT.md
    model: sonnet
    max_turns: 10
    topic_id: 1                        # discover IDs via `leo telegram topics`
    enabled: true

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

### `agent`

Identifies your workspace.

### `telegram`

Bot credentials and optional forum group/topic routing. The `bot_token` and `chat_id` are required for all messaging.

### `defaults`

Default model and max turns applied to all tasks unless overridden.

### `tasks`

Named tasks with cron schedules, prompt files, and optional overrides. Each task can override the default model and max turns.

---

See [Config Reference](config-reference.md) for the full field-by-field specification.
