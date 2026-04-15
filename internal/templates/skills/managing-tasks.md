# Managing Tasks

Tasks are scheduled Claude invocations defined in `leo.yaml` and executed by the Leo daemon's in-process scheduler.

## Task Lifecycle

### List tasks
```bash
leo task list
```
Shows all configured tasks with schedule, model, enabled status, last run, and next run. `NEXT RUN` is queried live from the daemon — if the daemon is not running, that column is blank.

### Add a task
```bash
leo task add
```
Interactive wizard prompts for: name, cron schedule, prompt file path, model override, channels, notify_on_fail, silent mode. Writes the entry to `leo.yaml`.

**⚠ `leo task add` does not notify the running daemon.** After adding a task, run `leo service reload` to register it with the live scheduler. Until you do, the task is in config but will never fire.

### Remove a task
```bash
leo task remove <name>
```
Removes the task from `leo.yaml`. When the daemon is running, the request routes through it and the scheduler is resynced automatically. Does not delete the prompt file.

### Enable / Disable
```bash
leo task enable <name>
leo task disable <name>
```
Toggles `enabled:` on the task. When the daemon is running, these route through it and trigger an immediate scheduler resync — no manual reload needed. Disabled tasks stay in config but are skipped by the scheduler.

## Applying Config Changes

The daemon reads `leo.yaml` at startup and holds a live copy. Some mutations auto-sync the scheduler; others require an explicit reload.

| Change                                 | Auto-syncs?         | Action required                  |
|----------------------------------------|---------------------|----------------------------------|
| `leo task remove` (daemon running)     | ✅ Yes              | None                             |
| `leo task enable` / `disable`          | ✅ Yes              | None                             |
| `leo task add`                         | ❌ No               | `leo service reload`             |
| Direct edits to `leo.yaml`             | ❌ No               | `leo service reload`             |
| Changes to `defaults:` or `processes:`            | ❌ No    | `leo service reload`             |

### Hot-reload
```bash
leo service reload
```
Reloads `leo.yaml` in-place and calls a full scheduler resync. Supervised processes (including your assistant session) keep running uninterrupted.

### Full restart (only when necessary)
```bash
leo service restart
```
Stops the daemon and all supervised processes, then starts everything fresh. Use only when `reload` isn't enough — e.g., to restart a stuck assistant session or apply changes that reload doesn't cover (process definitions being swapped out underneath a running supervisor can misbehave).

### Gotcha: task enabled but never fires
If `leo task list` shows a task as `enabled` with a correct `NEXT RUN`, but it never actually runs, the most common cause is that the task was added via a direct `leo.yaml` edit (or via `leo task add`) and `leo service reload` was never called. On-disk config and live scheduler state have diverged silently.

Always run `leo service reload` after editing `leo.yaml` by hand.

## Cron Schedule Syntax

Five-field format: `minute hour day-of-month month day-of-week`

| Field         | Values    | Example |
|---------------|-----------|---------|
| Minute        | 0-59      | `30`    |
| Hour          | 0-23      | `9`     |
| Day of month  | 1-31      | `1`     |
| Month         | 1-12      | `*`     |
| Day of week   | 0-6 (Sun=0) | `1-5` |

### Common schedules
```
0 9 * * *       # Daily at 9:00 AM
0 9 * * 1-5     # Weekdays at 9:00 AM
*/30 * * * *    # Every 30 minutes
0 */2 * * *     # Every 2 hours
0 9 * * 1       # Mondays at 9:00 AM
0 9,18 * * *    # 9 AM and 6 PM daily
```

Each task takes a single 5-field expression. Compound expressions (multiple schedules joined together, e.g. `0 9 * * 1-5,0 10 * * 0,6`) are **not** supported. Split differing schedules across multiple tasks, or use a single expression that covers all firing times (`0 9,10 * * *` rather than two separate "weekday 9am" + "weekend 10am" clauses).

## Prompt Files

Each task has a `prompt_file` (relative to workspace) containing the instructions for that run. Create prompt files under `prompts/` or `reports/`:

```
prompts/
├── daily-briefing.md
├── weekly-review.md
└── heartbeat.md
```

The prompt file content is assembled with:
1. Silent preamble (if `silent: true`)
2. Your prompt file content

The agent is responsible for delivering its final message via a configured channel plugin (see `$LEO_CHANNELS`). If no channel is configured, the agent should output `NO_REPLY`.

## Running a Task Manually

```bash
leo run <task>
```
Executes the task immediately, outside the schedule. Useful for testing and for backfilling a missed run.

```bash
leo run <task> --dry-run
```
Shows the assembled prompt without executing. Good for verifying prompt assembly.

## Legacy `leo cron` Commands

`leo cron` is a hidden compatibility layer from the era when tasks were written to system crontab:

| Legacy command      | Modern equivalent                                         |
|---------------------|-----------------------------------------------------------|
| `leo cron install`  | `leo service reload`                                      |
| `leo cron list`     | `leo task list` (legacy prints a hint, no actual listing) |
| `leo cron remove`   | Unregisters all scheduled tasks from the running daemon   |

Prefer the modern commands in new workflows.
