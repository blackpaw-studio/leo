# leo task

Manage scheduled tasks.

## Usage

```bash
leo task list               # list configured tasks
leo task add                # add a new task interactively
leo task enable <name>      # enable a task
leo task disable <name>     # disable a task
```

## Subcommands

### `leo task list`

Displays all configured tasks with their schedule, model, and enabled status:

```
heartbeat                 0,30 7-22 * * *      sonnet   enabled
daily-news-briefing       0 7 * * *            opus     enabled
weekly-report             0 9 * * 1            sonnet   disabled
```

### `leo task add`

Interactively prompts for task details:

- **Task name** — unique identifier for the task
- **Cron schedule** — when to run (e.g., `0 7 * * *`)
- **Prompt file** — path relative to workspace (e.g., `reports/daily-briefing.md`)
- **Model** — Claude model override (leave blank for default)
- **Telegram topic** — optional topic key for forum routing
- **Silent mode** — whether to prepend the silent preamble

The task is saved to `leo.yaml` with `enabled: true`.

!!! tip "Don't forget to install cron"
    After adding a task, run `leo cron install` to update your system crontab.

### `leo task enable <name>`

Enables a previously disabled task. Updates the `enabled` field in `leo.yaml`.

### `leo task disable <name>`

Disables a task. The task definition remains in the config but won't be included when running `leo cron install`.

## See Also

- [`leo cron`](cron.md) — install tasks to the system crontab
- [`leo run`](run.md) — execute a task manually
- [Writing Tasks](../guides/writing-tasks.md) — how to write task prompt files
- [Config Reference](../configuration/config-reference.md#tasks) — full task field specification
