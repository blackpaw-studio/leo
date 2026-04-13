# Managing Tasks

Tasks are scheduled Claude invocations defined in `leo.yaml` and executed by the Leo daemon's internal scheduler.

## Task Lifecycle

### List tasks
```bash
leo task list
```
Shows all configured tasks with schedule, model, enabled status, last run, and next run.

### Add a task
```bash
leo task add
```
Interactive wizard prompts for: name, cron schedule, prompt file path, model override, Telegram topic, silent mode. Creates the task entry in `leo.yaml` and optionally creates the prompt file.

### Remove a task
```bash
leo task remove <name>
```
Removes the task from `leo.yaml`. Does not delete the prompt file.

### Enable / Disable
```bash
leo task enable <name>
leo task disable <name>
```
Disabled tasks stay in config but are skipped by the scheduler.

## Applying Config Changes

**IMPORTANT:** After any edit to `leo.yaml` — whether via `leo task` commands or editing the file directly — the running daemon needs to reload the config to pick up the changes.

### Hot-reload (preferred)
```bash
leo service reload
```
Reloads `leo.yaml` in-place. The scheduler picks up new tasks, schedule changes, and enable/disable toggles **without restarting the daemon**. Supervised processes (including your assistant session) keep running uninterrupted.

Run this after:
- Adding a new task (`leo task add` or editing `leo.yaml`)
- Changing a task's schedule
- Enabling or disabling a task
- Editing `defaults:`, `telegram:`, or `processes:` blocks

### Full restart (only when necessary)
```bash
leo service restart
```
Stops the daemon and all supervised processes, then starts everything fresh. Use only when `reload` isn't enough — e.g., to restart a stuck assistant session or apply changes that the reload path doesn't cover.

### Gotcha: tasks that never fire
If a task is configured but `leo task list` shows it with no "LAST RUN" and a correct "NEXT RUN", but it never actually runs — the most common cause is that the task was added **after** the daemon started and `leo service reload` was never called. Config on disk and live scheduler state diverge silently.

Always run `leo service reload` after editing `leo.yaml`.

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

Leo uses a standard 5-field expression — compound expressions like `0 9 * * 1-5,0 10 * * 0,6` are **not** supported. Split differing schedules across multiple tasks or use a single expression that covers all firing times.

## Prompt Files

Each task has a `prompt_file` (relative to workspace) containing the instructions for that run. Create prompt files in `reports/` or `prompts/`:

```
prompts/
├── daily-briefing.md
├── weekly-review.md
└── heartbeat.md
```

The prompt file content is assembled with:
1. Silent preamble (if `silent: true`)
2. Your prompt file content
3. Telegram notification protocol (injected automatically)

## Running a Task Manually

```bash
leo run <task>
```
Executes the task immediately (same as the scheduler would). Useful for testing and for backfilling a missed run.

```bash
leo run <task> --dry-run
```
Shows the assembled prompt without executing. Good for verifying prompt assembly.

## Legacy `leo cron` Commands

`leo cron install` / `leo cron list` / `leo cron remove` are retained as aliases for backward compatibility:

| Legacy command      | Modern equivalent   |
|---------------------|---------------------|
| `leo cron install`  | `leo service reload`|
| `leo cron list`     | `leo task list`     |
| `leo cron remove`   | unregister all scheduled tasks from the daemon |

Prefer the modern commands in new workflows.
