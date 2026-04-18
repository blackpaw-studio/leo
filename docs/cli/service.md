# leo service

Manage persistent Claude sessions.

## Usage

```bash
# Run a single process in the foreground
leo service [process-name]

# Background with auto-restart (all enabled processes)
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

`leo service` manages long-running Claude sessions for the processes defined in your config. Each process can have its own workspace, channels, model, and settings.

When run in **supervised mode** (via `leo service start`), Leo starts all enabled processes and restarts them on crash with exponential backoff.

When run in **foreground mode** (via `leo service [process-name]`), Leo starts a single process. If no name is given, it picks the first enabled process.

## Subcommands

### `leo service start`

Starts all enabled processes in the background with automatic restart on crash. Uses exponential backoff (5s initial, 60s max) to avoid rapid restart loops.

**Flags:**

| Flag | Description |
|------|-------------|
| `--daemon` | Install as an OS service (launchd on macOS, systemd on Linux) instead of a simple background process. Persists across reboots. |

### `leo service stop`

Stops all running background processes.

**Flags:**

| Flag | Description |
|------|-------------|
| `--daemon` | Remove the OS service instead of stopping a background process. |

### `leo service status`

Shows whether the service is currently running.

**Flags:**

| Flag | Description |
|------|-------------|
| `--daemon` | Check OS service status instead of background process status. |

### `leo service restart`

Restarts the daemon.

### `leo service logs`

Tail the service log file.

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-n`, `--tail` | `50` | Number of lines to display. |
| `-f`, `--follow` | `false` | Follow the log output (like `tail -f`). |

## Claude Arguments

For each process, Leo builds `claude` arguments based on the process config:

```
claude --channels <channels>               \
       --add-dir <workspace>               \
       --add-dir <extra-dirs...>           \    # if add_dirs configured
       --remote-control <process-name>     \    # if remote_control enabled
       --dangerously-skip-permissions      \    # if bypass_permissions enabled
       --mcp-config <mcp-config-path>      \    # if MCP servers exist
       --session-id <id> | --resume <id>        # session persistence
```

## Logs

All modes write logs to `~/.leo/state/service.log`. The supervised child rotates this file automatically on size: when it reaches 10 MB, [lumberjack](https://pkg.go.dev/gopkg.in/natefinch/lumberjack.v2) renames it to a timestamped backup (`service-<timestamp>.log.gz`) and opens a fresh file in place. Up to 3 backups are retained for 30 days, gzipped. No external logrotate setup is required. `leo service logs -f` reopens cleanly across rotations.

## Service Labels

- **macOS (launchd):** `com.blackpaw.leo`
- **Linux (systemd):** `leo.service`

## See Also

- [Background Mode](../guides/background-mode.md) -- detailed comparison of background vs daemon mode
- [Configuration → Channels](../configuration/config-reference.md#channels) — wiring up a channel plugin
