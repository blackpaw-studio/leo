# leo cron (deprecated)

!!! warning "Hidden compatibility shim"
    `leo cron` is a hidden command kept for backwards compatibility with earlier releases that wrote tasks to the system crontab. It no longer touches the system crontab — Leo runs its own in-process scheduler inside the daemon.

    Use [`leo service reload`](service.md) and [`leo task`](task.md) instead.

## Mapping

| Old command | Current equivalent |
|-------------|--------------------|
| `leo cron install` | `leo service reload` (reload the daemon scheduler after editing `leo.yaml`) |
| `leo cron remove`  | `leo service stop` (scheduler stops with the daemon) |
| `leo cron list`    | `leo task list` (shows schedules with next run times) |

## See Also

- [`leo task`](task.md) — the canonical interface for managing scheduled tasks
- [`leo service`](service.md) — starting, stopping, and reloading the daemon
- [Scheduling guide](../guides/scheduling.md) — how the in-process scheduler works
