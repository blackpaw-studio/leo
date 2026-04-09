# leo cron

Manage cron entries for scheduled tasks.

## Usage

```bash
leo cron install    # install all enabled tasks to system crontab
leo cron remove     # remove all Leo-managed cron entries
leo cron list       # show installed schedules
```

## Subcommands

### `leo cron install`

Reads all enabled tasks from your config and writes them to the system crontab using a marker-delimited block:

```cron
# === LEO — DO NOT EDIT ===
# leo:heartbeat
0,30 7-22 * * * leo run heartbeat --config /path/to/leo.yaml >> /path/to/state/heartbeat.log 2>&1
# leo:daily-news-briefing
0 7 * * * leo run daily-news-briefing --config /path/to/leo.yaml >> /path/to/state/daily-news-briefing.log 2>&1
# === END LEO ===
```

Running `install` again replaces the existing block — it's safe to re-run after config changes.

### `leo cron remove`

Removes all Leo-managed cron entries from the system crontab. Other cron entries are left untouched.

### `leo cron list`

Displays the current Leo cron block from the system crontab. Shows nothing if no entries are installed.

## Workflow

After adding, removing, or modifying tasks:

```bash
leo task add                # add a new task interactively
leo cron install            # update crontab with new entries
leo cron list               # verify
```

## See Also

- [`leo task`](task.md) — manage task definitions
- [`leo run`](run.md) — what cron actually calls
- [Scheduling](../guides/scheduling.md) — cron expressions and timezone handling
