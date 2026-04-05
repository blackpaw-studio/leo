# Background Mode

For production use, you'll want the Telegram chat session to stay alive and restart automatically if it crashes. Leo offers two options.

## Simple Background Mode

Spawns a supervised process with automatic restart and exponential backoff. No OS-level installation required.

```bash
leo chat start            # start in background
leo chat status           # check if running
leo chat stop             # stop the session
```

**How it works:**

- Leo spawns itself with an internal `--supervised` flag
- If Claude crashes, Leo waits and restarts automatically
- Backoff starts at 5 seconds, doubles on each consecutive failure, caps at 60 seconds
- Resets after a successful run period
- PID is written to `<workspace>/state/chat.pid`
- Logs go to `<workspace>/state/chat.log`

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
leo chat start --daemon   # install and start as OS service
leo chat status --daemon  # check daemon status
leo chat stop --daemon    # uninstall OS service
```

### macOS (launchd)

Leo creates a launchd plist at `~/Library/LaunchAgents/com.blackpaw.leo.<name>.plist` and loads it. The service:

- Starts automatically on login
- Restarts automatically on crash
- Captures environment variables (`ANTHROPIC_API_KEY`, `HOME`, `PATH`, etc.)

### Linux (systemd)

Leo creates a systemd user unit at `~/.config/systemd/user/leo-<name>.service` and enables it. The service:

- Starts automatically on login
- Restarts automatically on crash (with 5-second delay)
- Runs as a user-level service (no root required)

**Pros:**

- Survives reboots and logouts
- OS-managed restarts
- Standard service management tools (`launchctl`, `systemctl`)

**Cons:**

- Requires initial installation step
- Environment variable changes require reinstalling (`leo chat start --daemon` again)

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
tail -f ~/leo/state/chat.log
```

## Environment Variables

Daemon mode captures your current environment variables at installation time. If you update variables like `ANTHROPIC_API_KEY`, reinstall the daemon:

```bash
leo chat stop --daemon
leo chat start --daemon
```

## See Also

- [`leo chat`](../cli/chat.md) — full command reference
- [Telegram Setup](telegram-setup.md) — configuring your bot
