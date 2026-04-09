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

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `model` | string | No | Default Claude model (`sonnet`, `opus`, `haiku`). Defaults to `sonnet`. |
| `max_turns` | int | No | Default maximum agent turns per execution. Defaults to `15`. |
| `bypass_permissions` | bool | No | Enable `--dangerously-skip-permissions` on claude sessions. Default `false`. |
| `remote_control` | bool | No | Enable `--remote-control` on claude sessions for web/mobile access via claude.ai/code. Default `false`. |

## `processes`

Each process is a named entry under the `processes` map. Processes define long-running Claude sessions (e.g., a Telegram listener).

```yaml
processes:
  <process-name>:
    channels:
      - plugin:telegram@claude-plugins-official
    enabled: true
    # ... fields below
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `workspace` | string | No | `~/.leo/workspace/` | Workspace directory for this process. |
| `channels` | list | No | — | Claude channel plugins to enable (e.g., `plugin:telegram@claude-plugins-official`). |
| `model` | string | No | `defaults.model` | Claude model override for this process. |
| `max_turns` | int | No | `defaults.max_turns` | Max turns override for this process. |
| `bypass_permissions` | bool | No | `defaults.bypass_permissions` | Override bypass_permissions for this process. |
| `remote_control` | bool | No | `defaults.remote_control` | Override remote_control for this process. |
| `mcp_config` | string | No | `<workspace>/config/mcp-servers.json` | Path to MCP config file. Relative paths resolve from the process workspace. |
| `add_dirs` | list | No | — | Additional directories to pass via `--add-dir`. |
| `enabled` | bool | No | `false` | Whether this process is active. |

### Override Cascade

Process settings override defaults:

```
effective model             = process.model             OR defaults.model
effective max_turns         = process.max_turns         OR defaults.max_turns
effective bypass_permissions = process.bypass_permissions OR defaults.bypass_permissions
effective remote_control    = process.remote_control    OR defaults.remote_control
```

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
| `workspace` | string | No | `~/.leo/workspace/` | Workspace directory for this task. |
| `schedule` | string | Yes | — | Cron expression (e.g., `"0 7 * * *"`). |
| `timezone` | string | No | System default | IANA timezone (e.g., `America/New_York`). |
| `prompt_file` | string | Yes | — | Path to prompt file, relative to workspace. |
| `model` | string | No | `defaults.model` | Claude model override for this task. |
| `max_turns` | int | No | `defaults.max_turns` | Max turns override for this task. |
| `topic_id` | int | No | — | Telegram forum topic ID (discover via `leo telegram topics`). |
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
