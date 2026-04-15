# leo task

Manage scheduled tasks.

## Usage

```bash
leo task list                 # list configured tasks
leo task add                  # add a new task interactively
leo task remove <name>        # remove a task from the config
leo task enable <name>        # enable a task
leo task disable <name>       # disable a task
leo task history [name]       # show execution history (all tasks or one)
leo task logs <name>          # show the latest run's log output
```

Task names support shell tab-completion for `remove`, `enable`, `disable`, `history`, and `logs`.

## Subcommands

### `leo task list`

Displays all configured tasks with schedule, model, enabled status, last run result, and the next scheduled run (when the daemon is running):

```
  NAME                 SCHEDULE           MODEL    STATUS   LAST RUN             NEXT RUN
  heartbeat            0,30 7-22 * * *    sonnet   enabled  Apr 13 08:30 ok      Apr 13 09:00
  daily-briefing       0 7 * * *          opus     enabled  Apr 13 07:00 ok      Apr 14 07:00
  weekly-report        0 9 * * 1          sonnet   disabled                      
```

### `leo task add`

Interactively prompts for task details:

- **Task name** — unique identifier for the task
- **Cron schedule** — when to run (e.g., `0 7 * * *`)
- **Prompt file** — path relative to the task's workspace
- **Model** — Claude model override (blank = default)
- **Channels** — optional list of channel plugin IDs for `notify_on_fail` (see [Channels](../configuration/config-reference.md#channels))
- **Silent mode** — whether to prepend the silent preamble

The task is saved to `leo.yaml` with `enabled: true`. If the prompt file does not exist yet, Leo prints a warning so you remember to author it before the first scheduled run.

### `leo task remove <name>`

Removes the task from the config. When the daemon is running, the removal is sent over the daemon socket so the scheduler drops the entry without a restart.

### `leo task enable <name>` / `leo task disable <name>`

Toggles the task `enabled` flag. When the daemon is running, the change is forwarded to the scheduler immediately via the daemon IPC. Otherwise the config is saved and the change is picked up the next time the scheduler reads it.

### `leo task history [name]`

Without a name, shows a one-row-per-task summary of the most recent run:

```
  TASK                 RUNS  LAST RUN
  heartbeat            412   Apr 13 08:30 ok
  daily-briefing       87    Apr 13 07:00 ok
  weekly-report        12    Apr 12 09:00 FAIL (timeout, exit 124)
```

With a task name, shows the full history for that task:

```
History for "daily-briefing" (last 30 runs):

  2026-04-13 07:00:15  ok
  2026-04-12 07:00:09  ok
  2026-04-11 07:00:12  FAIL (non-zero exit, exit 1)
```

Failures include a typed reason (`timeout`, `non-zero exit`, `crash`) alongside the exit code.

### `leo task logs <name>`

Prints the captured log output from the most recent run of the task.

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-n`, `--tail` | `0` | Show only the last N lines (`0` = show everything). |

Logs live under `~/.leo/state/logs/` and are written every time the task runs.

## See Also

- [`leo run`](run.md) — execute a task manually, independent of the schedule
- [Writing Tasks](../guides/writing-tasks.md) — how to write task prompt files
- [Scheduling](../guides/scheduling.md) — cron syntax, timezones, retries
- [Config Reference](../configuration/config-reference.md#tasks) — full task field specification
