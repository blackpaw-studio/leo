# Scheduling

Leo runs scheduled tasks from an in-process scheduler inside the daemon. This guide covers cron expressions, timezone handling, silent mode, and monitoring.

## How Scheduling Works

There is no system crontab. The `leo` daemon embeds a cron scheduler (via [robfig/cron](https://github.com/robfig/cron)) that reads your task definitions directly from `leo.yaml`:

1. You define tasks with cron expressions in `leo.yaml`
2. `leo service start` starts the daemon; the scheduler reads enabled tasks
3. At each fire time, the scheduler invokes the task runner in-process (equivalent to `leo run <task>`)
4. Leo assembles a prompt and invokes `claude -p` in non-interactive mode
5. The agent does its work and optionally delivers a message via a configured channel plugin

```
daemon scheduler --> leo run <task> --> claude -p "<prompt>" --> Agent --> channel plugin (optional)
```

Config changes (adds, edits, toggles) are picked up automatically by the web UI and the `leo task` commands — they reload the scheduler over the daemon socket without a restart. When you edit `leo.yaml` by hand, run `leo service reload` (or restart the daemon) so the scheduler sees the change.

## Cron Expressions

Leo uses standard 5-field cron syntax:

```
┌───────────── minute (0-59)
│ ┌───────────── hour (0-23)
│ │ ┌───────────── day of month (1-31)
│ │ │ ┌───────────── month (1-12)
│ │ │ │ ┌───────────── day of week (0-7, 0 and 7 are Sunday)
│ │ │ │ │
* * * * *
```

### Common Examples

| Expression | Description |
|-----------|-------------|
| `0 7 * * *` | Every day at 7:00 AM |
| `0,30 7-22 * * *` | Every 30 minutes from 7 AM to 10:30 PM |
| `0 9 * * 1-5` | Weekdays at 9:00 AM |
| `0 */4 * * *` | Every 4 hours |
| `0 7 * * 1` | Mondays at 7:00 AM |
| `*/15 * * * *` | Every 15 minutes |

## Timezones

Each task can specify an IANA timezone. The in-process scheduler honors it directly — no system-cron workarounds required:

```yaml
tasks:
  morning-briefing:
    schedule: "0 7 * * *"
    timezone: America/New_York
```

Without a `timezone`, the schedule is evaluated against the daemon's local time.

## Silent Mode

When `silent: true` is set on a task, Leo prepends a preamble to the prompt that instructs the agent to:

- Work without narration or progress updates
- Only deliver a message via a configured channel plugin if there's something meaningful to report
- Output `NO_REPLY` if there's nothing noteworthy

This is useful for tasks that run frequently where you only want to hear from the agent when something requires your attention.

```yaml
tasks:
  checks:
    schedule: "0,30 7-22 * * *"
    silent: true        # only messages you when something needs attention
```

## Monitoring

### Check the schedule

```bash
leo task list       # shows each task's next scheduled run (when daemon is running)
leo status          # also shows the single earliest upcoming run
```

### View run history

```bash
leo task history               # one-row summary per task
leo task history <task-name>   # full history for one task
leo task logs <task-name>      # tail the most recent run's log
```

Run logs live under `~/.leo/state/logs/`.

### Manual test run

Run any task manually to verify it works:

```bash
leo run checks
```

## Applying Config Changes

- **Edits via CLI** (`leo task add/remove/enable/disable`) reload the scheduler automatically when the daemon is running.
- **Edits via the web UI** reload automatically on save; if the reload fails (e.g. an invalid cron expression slipped past initial validation) the UI surfaces a warning flash describing the error.
- **Manual edits to `leo.yaml`** need a nudge:
    ```bash
    leo service reload     # or: restart the daemon
    ```

## Best Practices

- **Start conservative** — begin with 1–2 tasks and add more as you tune your prompts
- **Use silent mode** for frequent tasks to avoid notification fatigue
- **Check logs** after the first few runs (`leo task logs <name>`) to verify the agent is behaving as expected
- **Mind rate limits** — running many tasks frequently consumes API tokens. Space out non-urgent tasks
- **Let the channel plugin route messages** — if you use a forum-aware plugin (e.g. Telegram topics, Slack threads), route each task to the right topic/thread from within the agent prompt rather than hardcoding routing in leo.yaml

## See Also

- [`leo task`](../cli/task.md) — managing task definitions, history, and logs
- [`leo run`](../cli/run.md) — executing tasks manually
- [Writing Tasks](writing-tasks.md) — creating custom task prompts
