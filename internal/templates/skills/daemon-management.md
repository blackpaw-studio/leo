# Daemon Management

The persistent session (`leo service`) runs Claude with any channel plugins you have configured and optional Remote Control. It can run in foreground, background, or as an OS service.

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

## Graceful Restarts

`leo service restart`, `leo update` and the web UI's restart button can drain busy agents before restarting the daemon. If you are mid-turn when that happens, you will see these messages:

- **`[leo] URGENT: the leo daemon restarts in N minutes and this session may be killed. ...`** — stop now. Do not start new steps, tool calls or dispatches. Finish or abandon the current step, save or commit work in progress, write a short note of where you are and what's next, then end your turn. The restart waits until you are idle (up to the stated time), so ending the turn promptly is what lets it proceed. Anything unsaved when the restart happens may be lost.
- **`[leo] Daemon restarted. Resume where you left off.`** — the restart finished. Re-read your note and continue.
- **`[leo] Restart cancelled. Resume your work.`** — the restart was called off (aborted, interrupted or failed). Continue as if the pause never happened.

Cron tasks due during a drain are deferred and run once each afterwards. Dispatch runs are not messaged; your own drain covers any dispatch you started.

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

The background mode wraps Claude in a tmux session so channel plugins that need a terminal (stdio MCP servers, bot runners, etc.) have one available.

```bash
tmux ls                          # List sessions
tmux attach -t leo-<pid>        # Attach to see live output (Ctrl+B D to detach)
```

## Environment Variables

The daemon captures these from your shell at install time:
- `ANTHROPIC_API_KEY`
- `HOME`, `PATH`, `SHELL`, `USER`
- `CLAUDE_CODE_ENTRYPOINT` (set to "cli")

At spawn time the supervisor also injects:
- `LEO_CHANNELS` — comma-separated list of channel plugin IDs for this process

PATH is augmented with `/opt/homebrew/bin` if it exists.

If environment changes (e.g., new API key), restart the daemon:
```bash
leo service stop --daemon && leo service start --daemon
```

## Troubleshooting

### Daemon won't start
1. Check `leo validate` for prereq issues (claude, tmux)
2. Check logs: `tail -50 state/service.log`
3. Verify API key: `echo $ANTHROPIC_API_KEY`
4. Try foreground mode to see errors: `leo service`

### Daemon keeps restarting
The restart loop uses exponential backoff. Check `state/service.log` for the crash reason. Common causes:
- API key expired or rate-limited
- Network connectivity issues
- A channel plugin failing to load or connect (check the plugin's own logs)

### Session feels stale
The session accumulates context over time. Restarting clears it:
```bash
leo service restart
```
