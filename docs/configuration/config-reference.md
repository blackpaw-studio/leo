# Config Reference

Complete field-by-field reference for `leo.yaml`.

## `agent`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Agent name. Used for the Claude agent file, cron markers, and display. |
| `workspace` | string | Yes | Workspace directory path. Supports `~` expansion. |
| `agent_file` | string | No | Custom path to the Claude agent file. Defaults to `~/.claude/agents/<name>.md`. |

## `telegram`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `bot_token` | string | Yes | Telegram Bot API token from [@BotFather](https://t.me/BotFather). |
| `chat_id` | string | Yes | Your personal Telegram chat ID. Auto-detected during setup. |
| `group_id` | string | No | Forum group chat ID (starts with `-100`). Enables topic routing. |
| `topics` | map[string]int | No | Maps topic names to Telegram `message_thread_id` values. |

### Topic Routing

When a task specifies `topic: alerts`, Leo looks up `telegram.topics.alerts` to get the `message_thread_id`. This routes the task's output to a specific forum topic in a Telegram group.

```yaml
telegram:
  group_id: "-1001234567890"
  topics:
    alerts: 1
    news: 3
    reports: 7
```

## `defaults`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `model` | string | Yes | Default Claude model (`sonnet`, `opus`, `haiku`). |
| `max_turns` | int | Yes | Default maximum agent turns per task execution. |

## `tasks`

Each task is a named entry under the `tasks` map:

```yaml
tasks:
  <task-name>:
    schedule: "0 7 * * *"
    # ... fields below
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `schedule` | string | Yes | — | Cron expression (e.g., `"0 7 * * *"`). |
| `timezone` | string | No | System default | IANA timezone (e.g., `America/New_York`). |
| `prompt_file` | string | Yes | — | Path to prompt file, relative to workspace. |
| `model` | string | No | `defaults.model` | Claude model override for this task. |
| `max_turns` | int | No | `defaults.max_turns` | Max turns override for this task. |
| `topic` | string | No | — | Telegram topic key (maps to `telegram.topics`). |
| `enabled` | bool | No | `false` | Whether cron should run this task. |
| `silent` | bool | No | `false` | Prepend silent-mode preamble to the prompt. |

### Override Cascade

Task settings override defaults. When Leo runs a task, it resolves the effective model and max turns:

```
effective model     = task.model     OR defaults.model
effective max_turns = task.max_turns OR defaults.max_turns
```

### Silent Mode

When `silent: true`, Leo prepends a preamble to the prompt instructing the agent to work without narration. The agent should either send a final Telegram message with results or output `NO_REPLY` if there's nothing to report.
