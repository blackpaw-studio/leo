# Daemon Management

The persistent session (`leo service`) runs Claude with the Telegram channel plugin and optional Remote Control. It can run in foreground, background, or as an OS service.

## Running Modes

### Foreground (development/testing)
```bash
leo service
```
Replaces the current process with Claude. Ctrl+C to stop. Good for debugging.

### Background (supervised)
```bash
leo service start
```
Runs Claude in a tmux session with automatic restart on crash. Uses exponential backoff (5s to 60s). PID written to `state/service.pid`.

```bash
leo service stop       # Stop the background session
leo service status     # Check if running (shows PID or "stopped")
```

### OS Service (persistent)
```bash
leo service start --daemon    # Install and start OS service
leo service stop --daemon     # Stop and remove OS service
leo service restart           # Restart the daemon
leo service status --daemon   # Check OS service status
```

## Platform Details

### macOS (launchd)
- **Plist**: `~/Library/LaunchAgents/com.blackpaw.leo.plist`
- Configured with `KeepAlive` and `RunAtLoad`
- Managed via `launchctl`

Check launchd directly:
```bash
launchctl print gui/$(id -u)/com.blackpaw.leo
```

### Linux (systemd)
- **Unit**: `~/.config/systemd/user/leo.service`
- Configured with `Restart=always`, `RestartSec=5`
- Managed via `systemctl --user`

Check systemd directly:
```bash
systemctl --user status leo
journalctl --user -u leo --since "1 hour ago"
```

## tmux Session

The background mode wraps Claude in a tmux session (required for the Telegram plugin's terminal communication).

```bash
tmux ls                          # List sessions
tmux attach -t leo-<pid>        # Attach to see live output (Ctrl+B D to detach)
```

## Environment Variables

The daemon captures these from your shell at install time:
- `ANTHROPIC_API_KEY`
- `HOME`, `PATH`, `SHELL`, `USER`
- `TELEGRAM_BOT_TOKEN`
- `CLAUDE_CODE_ENTRYPOINT` (set to "cli")

PATH is augmented with `~/.bun/bin` and `/opt/homebrew/bin` if they exist.

If environment changes (e.g., new API key), restart the daemon:
```bash
leo service stop --daemon && leo service start --daemon
```

## Troubleshooting

### Daemon won't start
1. Check `leo validate` for prereq issues (claude, tmux, bun)
2. Check logs: `tail -50 state/service.log`
3. Verify API key: `echo $ANTHROPIC_API_KEY`
4. Try foreground mode to see errors: `leo service`

### Daemon keeps restarting
The restart loop uses exponential backoff. Check `state/service.log` for the crash reason. Common causes:
- API key expired or rate-limited
- Network connectivity issues
- Telegram plugin failing to connect

### Session feels stale
The session accumulates context over time. Restarting clears it:
```bash
leo service restart
```
