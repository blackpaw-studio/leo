# leo service

Manage the interactive Telegram session.

## Usage

```bash
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

`leo service` manages a long-running Claude session with the official Telegram channel plugin. Your assistant listens for inbound Telegram messages and responds through the channel.

## Subcommands

### `leo service start`

Starts the session in the background with automatic restart on crash. Uses exponential backoff (5s initial, 60s max) to avoid rapid restart loops.

**Flags:**

| Flag | Description |
|------|-------------|
| `--daemon` | Install as an OS service (launchd on macOS, systemd on Linux) instead of a simple background process. Persists across reboots. |

### `leo service stop`

Stops a running background session.

**Flags:**

| Flag | Description |
|------|-------------|
| `--daemon` | Remove the OS service instead of stopping a background process. |

### `leo service status`

Shows whether the session is currently running.

**Flags:**

| Flag | Description |
|------|-------------|
| `--daemon` | Check OS service status instead of background process status. |

### `leo service restart`

Stops and restarts the background session.

**Flags:**

| Flag | Description |
|------|-------------|
| `--daemon` | Restart the OS service instead of the background process. |

### `leo service logs`

Tail the service log file.

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-n`, `--tail` | `50` | Number of lines to display. |
| `-f`, `--follow` | `false` | Follow the log output (like `tail -f`). |

## Claude Arguments

Leo builds the following `claude` arguments for the session:

```
claude --channels plugin:telegram@claude-plugins-official \
       --add-dir <workspace> \
       --remote-control \                                  # if defaults.remote_control: true
       --mcp-config <workspace>/config/mcp-servers.json  # if exists
```

## Logs

All modes write logs to `<workspace>/state/service.log`.

## Service Labels

- **macOS (launchd):** `com.blackpaw.leo`
- **Linux (systemd):** `leo.service`

## See Also

- [Background Mode](../guides/background-mode.md) — detailed comparison of background vs daemon mode
- [Telegram Setup](../guides/telegram-setup.md) — creating and configuring your bot
