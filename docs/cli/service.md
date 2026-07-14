# leo service

Manage the leo daemon.

## Usage

```bash
# Run the daemon in the foreground
leo service

# Background with auto-restart
leo service start
leo service stop
leo service status
leo service restart
leo service logs

# OS-level daemon (launchd/systemd)
leo service start --daemon
leo service stop --daemon
leo service status --daemon
```

## Description

`leo service` runs the leo daemon: the web UI, the cron scheduler, the daemon IPC server, and supervision for ephemeral agents — including the agents backing `runtime: persistent` tasks. On start it restores any agents that were running before the daemon last stopped (see `RestoreAgents`), then keeps each of them alive in its own tmux session with restart-on-crash and exponential backoff.

`leo service` no longer manages config-declared "processes" — agents (spawned via `leo agent spawn`, a template, or ensure-exists'd by a persistent task firing) are the only thing it supervises.

## Subcommands

### `leo service start`

Starts the daemon in the background with automatic restart on crash for supervised agents. Uses exponential backoff (5s initial, 60s max) to avoid rapid restart loops.

**Flags:**

| Flag | Description |
|------|-------------|
| `--daemon` | Install as an OS service (launchd on macOS, systemd on Linux) instead of a simple background process. Persists across reboots. |

### `leo service stop`

Stops the running daemon and tears down its supervised tmux sessions. Session IDs are preserved so a subsequent start resumes where each agent left off.

**Flags:**

| Flag | Description |
|------|-------------|
| `--daemon` | Remove the OS service instead of stopping a background process. |

### `leo service status`

Shows whether the daemon is currently running.

**Flags:**

| Flag | Description |
|------|-------------|
| `--daemon` | Check OS service status instead of background process status. |

### `leo service restart`

Restarts the daemon.

### `leo service logs`

Tail the daemon log file.

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-n`, `--tail` | `50` | Number of lines to display. |
| `-f`, `--follow` | `false` | Follow the log output (like `tail -f`). |

## Claude Arguments

For each supervised agent, Leo builds `claude` arguments based on its config:

```
claude --channels <channels>               \
       --add-dir <workspace>               \
       --add-dir <extra-dirs...>           \    # if add_dirs configured
       --remote-control <name>             \    # if harness_options.remote_control enabled
       --dangerously-skip-permissions      \    # if harness_options.bypass_permissions enabled
       --mcp-config <mcp-config-path>      \    # if MCP servers exist
       --session-id <id> | --resume <id>        # session persistence
```

## Logs

All modes write logs to `~/.leo/state/service.log`. The daemon rotates this file automatically on size: when it reaches 10 MB, [lumberjack](https://pkg.go.dev/gopkg.in/natefinch/lumberjack.v2) renames it to a timestamped backup (`service-<timestamp>.log.gz`) and opens a fresh file in place. Up to 3 backups are retained for 30 days, gzipped. No external logrotate setup is required. `leo service logs -f` reopens cleanly across rotations.

## Service Labels

- **macOS (launchd):** `com.blackpaw.leo`
- **Linux (systemd):** `leo.service`

## See Also

- [Background Mode](../guides/background-mode.md) -- detailed comparison of background vs daemon mode
- [Configuration → Channels](../configuration/config-reference.md#channels) — wiring up a channel plugin
