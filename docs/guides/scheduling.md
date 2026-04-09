# Scheduling

Leo uses system cron to run tasks on a schedule. This guide covers cron expressions, timezone handling, silent mode, and monitoring.

## How Scheduling Works

Leo is not a daemon for scheduled tasks. Instead:

1. You define tasks with cron expressions in `leo.yaml`
2. `leo cron install` writes entries to your system crontab
3. At the scheduled time, cron calls `leo run <task>`
4. Leo assembles a prompt and invokes `claude -p` in non-interactive mode
5. The agent does its work and optionally sends a Telegram message

```
cron --> leo run <task> --> claude -p "<prompt>" --> Agent --> Telegram (optional)
```

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

Each task can specify an IANA timezone:

```yaml
tasks:
  morning-briefing:
    schedule: "0 7 * * *"
    timezone: America/New_York
```

!!! note
    The `timezone` field is used by Leo to annotate the cron entry. System cron itself typically runs in the system timezone. If your system timezone differs from the task timezone, you may need to adjust the schedule accordingly or use a cron implementation that supports `CRON_TZ`.

## Silent Mode

When `silent: true` is set on a task, Leo prepends a preamble to the prompt that instructs the agent to:

- Work without narration or progress updates
- Only send a Telegram message if there's something meaningful to report
- Output `NO_REPLY` if there's nothing noteworthy

This is useful for tasks that run frequently where you only want to hear from the agent when something requires your attention.

```yaml
tasks:
  checks:
    schedule: "0,30 7-22 * * *"
    silent: true        # only messages you when something needs attention
```

## Monitoring

### Check installed entries

```bash
leo cron list
```

### View task logs

Each task logs its output to `~/.leo/state/<task>.log`:

```bash
tail -f ~/.leo/state/checks.log
```

### Manual test run

Run any task manually to verify it works:

```bash
leo run checks
```

## Best Practices

- **Start conservative** -- begin with 1-2 tasks and add more as you tune your prompts
- **Use silent mode** for frequent tasks to avoid notification fatigue
- **Check logs** after the first few runs to verify the agent is behaving as expected
- **Mind rate limits** -- running many tasks frequently consumes API tokens. Space out non-urgent tasks
- **Use topic routing** to organize different types of notifications in a Telegram forum group

## See Also

- [`leo cron`](../cli/cron.md) -- managing cron entries
- [`leo task`](../cli/task.md) -- managing task definitions
- [`leo run`](../cli/run.md) -- executing tasks
- [Writing Tasks](writing-tasks.md) -- creating custom task prompts
