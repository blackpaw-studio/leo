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

Reads all enabled tasks from your config and writes them to the system crontab. Each agent gets a marker-delimited block:

```cron
# === LEO:leo — DO NOT EDIT ===
# leo:leo:heartbeat
0,30 7-22 * * * leo run heartbeat --config /path/to/leo.yaml >> /path/to/state/heartbeat.log 2>&1
# leo:leo:daily-news-briefing
0 7 * * * leo run daily-news-briefing --config /path/to/leo.yaml >> /path/to/state/daily-news-briefing.log 2>&1
# === END LEO:leo ===
```

Running `install` again replaces the existing block — it's safe to re-run after config changes.

### `leo cron remove`

Removes all Leo-managed cron entries for the current agent from the system crontab. Other cron entries and other agents' blocks are left untouched.

### `leo cron list`

Displays the current Leo cron block from the system crontab. Shows nothing if no entries are installed.

## Multiple Agents

Leo uses marker comments (`# === LEO:<agent> ===`) to delimit each agent's block. Multiple agents can coexist in a single crontab without interfering with each other.

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
