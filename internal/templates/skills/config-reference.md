# Config Reference

Leo's config lives at `leo.yaml` in the workspace root.

## Full Structure

```yaml
agent:
  name: <string>              # Agent name (required) — used for service labels and cron markers
  workspace: <path>           # Workspace directory path (required)

telegram:
  bot_token: <string>         # Telegram Bot API token from @BotFather (required)
  chat_id: <string>           # Your personal chat ID (required)
  group_id: <string>          # Forum group chat ID (optional — use instead of chat_id for groups)

defaults:
  model: <string>             # Default model: sonnet, opus, or haiku (required)
  max_turns: <int>            # Default max conversation turns (required)
  bypass_permissions: <bool>  # Skip permission prompts in task runs (optional)
  remote_control: <bool>      # Enable Remote Control for web/mobile access (optional)

tasks:
  <task-name>:
    schedule: <cron-expr>     # 5-field cron expression (required)
    timezone: <string>        # IANA timezone, e.g. America/New_York (optional)
    prompt_file: <path>       # Path relative to workspace (required)
    model: <string>           # Override defaults.model (optional)
    max_turns: <int>          # Override defaults.max_turns (optional)
    topic_id: <int>           # Telegram forum topic ID (optional — discover via `leo telegram topics`)
    enabled: <bool>           # Whether cron runs this task (default: true)
    silent: <bool>            # Suppress narration, output NO_REPLY if nothing to report (optional)
```

## Override Cascade

Task settings inherit from defaults and can be overridden per task:

```
effective model     = task.model     OR defaults.model
effective max_turns = task.max_turns OR defaults.max_turns
```

## Valid Models

- `sonnet` — Best for general coding and development tasks
- `opus` — Deepest reasoning, best for complex analysis
- `haiku` — Fastest and cheapest, good for simple checks

## Telegram Topics

Topics route messages to specific threads in a Telegram forum group. Use `leo telegram topics` to discover available topic IDs for your group, then reference them directly in tasks via `topic_id: <id>`.

If `group_id` is set, messages go to the group. The `topic_id` field adds a `message_thread_id` to route to a specific thread. If no `topic_id` is specified, messages go to the General thread.

## Paths

- Paths in `agent.workspace` support `~` expansion
- `prompt_file` is relative to the workspace directory
- Config is auto-discovered by searching up from the current directory, or specify with `--config`

## Validation

```bash
leo validate
```

Checks: required fields, model names, cron syntax, telegram consistency, topic IDs, file existence.
