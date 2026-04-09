# Background Mode

For production use, you'll want the Telegram session to stay alive and restart automatically if it crashes. Leo offers two options.

## Simple Background Mode

Spawns a supervised process with automatic restart and exponential backoff. No OS-level installation required.

```bash
leo service start            # start in background
leo service status           # check if running
leo service stop             # stop the session
```

**How it works:**

- Leo spawns itself with an internal `--supervised` flag
- If Claude crashes, Leo waits and restarts automatically
- Backoff starts at 5 seconds, doubles on each consecutive failure, caps at 60 seconds
- Resets after a successful run period
- PID is written to `~/.leo/state/service.pid`
- Logs go to `~/.leo/state/service.log`

**Pros:**

- Simple — no OS configuration needed
- Works immediately on any platform
- Easy to start and stop

**Cons:**

- Doesn't survive reboots
- Doesn't survive terminal session logout (unless run via `nohup` or `tmux`)

## Daemon Mode

Installs an OS-level service for supervision that persists across reboots.

```bash
leo service start --daemon   # install and start as OS service
leo service status --daemon  # check daemon status
leo service stop --daemon    # uninstall OS service
```

### macOS (launchd)

Leo creates a launchd plist at `~/Library/LaunchAgents/com.blackpaw.leo.plist` and loads it. The service:

- Starts automatically on login
- Restarts automatically on crash
- Captures environment variables (`ANTHROPIC_API_KEY`, `HOME`, `PATH`, etc.)

### Linux (systemd)

Leo creates a systemd user unit at `~/.config/systemd/user/leo.service` and enables it. The service:

- Starts automatically on login
- Restarts automatically on crash (with 5-second delay)
- Runs as a user-level service (no root required)

**Pros:**

- Survives reboots and logouts
- OS-managed restarts
- Standard service management tools (`launchctl`, `systemctl`)

**Cons:**

- Requires initial installation step
- Environment variable changes require reinstalling (`leo service start --daemon` again)

## Choosing Between Modes

| Feature | Simple | Daemon |
|---------|--------|--------|
| Auto-restart on crash | Yes | Yes |
| Survives reboot | No | Yes |
| Survives logout | No | Yes |
| OS installation | None | launchd/systemd |
| Setup complexity | Minimal | Low |

**Use simple mode** for development, testing, or when you'll be actively using your machine.

**Use daemon mode** for always-on operation, especially on servers or machines that restart.

## Logs

Both modes write to the same log file:

```bash
tail -f ~/.leo/state/service.log
```

Or use the built-in logs command:

```bash
leo service logs -f
```

## Environment Variables

Daemon mode captures your current environment variables at installation time. If you update variables like `ANTHROPIC_API_KEY`, reinstall the daemon:

```bash
leo service stop --daemon
leo service start --daemon
```

## See Also

- [`leo service`](../cli/service.md) — full command reference
- [Telegram Setup](telegram-setup.md) — configuring your bot
