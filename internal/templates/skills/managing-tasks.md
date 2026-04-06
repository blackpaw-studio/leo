# Managing Tasks

Tasks are scheduled Claude invocations defined in `leo.yaml` and executed by system cron via `leo run <task>`.

## Task Lifecycle

### List tasks
```bash
leo task list
```
Shows all configured tasks with schedule, model, enabled status.

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
Disabled tasks stay in config but are skipped by `leo cron install`.

## Cron Management

Tasks run via system crontab. Leo manages a marked block in your crontab.

### Install cron entries
```bash
leo cron install
```
Writes crontab entries for all **enabled** tasks. Must re-run after adding, removing, enabling, or disabling tasks.

### Remove cron entries
```bash
leo cron remove
```
Strips all Leo-managed entries from crontab.

### View installed entries
```bash
leo cron list
```

### Verify crontab directly
```bash
crontab -l
```
Leo entries are delimited by marker comments: `# === LEO:<agent> ===` / `# === END LEO:<agent> ===`

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

## Prompt Files

Each task has a `prompt_file` (relative to workspace) containing the instructions for that run. Create prompt files in `reports/`:

```
reports/
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
Executes the task immediately (same as cron would). Useful for testing.

```bash
leo run <task> --dry-run
```
Shows the assembled prompt without executing. Good for verifying prompt assembly.
