# leo chat

Start an interactive Telegram session.

## Usage

```bash
# Foreground (replaces current process)
leo chat

# Background with auto-restart
leo chat start
leo chat stop
leo chat status

# OS-level daemon (launchd/systemd)
leo chat start --daemon
leo chat stop --daemon
leo chat status --daemon
```

## Description

`leo chat` launches a long-running Claude session with the official Telegram channel plugin. Your agent listens for inbound Telegram messages and responds through the channel.

In its default foreground mode, `leo chat` replaces the current process via `exec` — it does not return. For persistent operation, use the `start` subcommand.

## Subcommands

### `leo chat start`

Starts the chat session in the background with automatic restart on crash. Uses exponential backoff (5s initial, 60s max) to avoid rapid restart loops.

**Flags:**

| Flag | Description |
|------|-------------|
| `--daemon` | Install as an OS service (launchd on macOS, systemd on Linux) instead of a simple background process. Persists across reboots. |

### `leo chat stop`

Stops a running background chat session.

**Flags:**

| Flag | Description |
|------|-------------|
| `--daemon` | Remove the OS service instead of stopping a background process. |

### `leo chat status`

Shows whether a chat session is currently running.

**Flags:**

| Flag | Description |
|------|-------------|
| `--daemon` | Check OS service status instead of background process status. |

## Claude Arguments

Leo builds the following `claude` arguments for the chat session:

```
claude --agent <name> \
       --channels plugin:telegram@claude-plugins-official \
       --add-dir <workspace> \
       --mcp-config <workspace>/config/mcp-servers.json  # if exists
```

## Logs

All modes write logs to `<workspace>/state/chat.log`.

## See Also

- [Background Mode](../guides/background-mode.md) — detailed comparison of background vs daemon mode
- [Telegram Setup](../guides/telegram-setup.md) — creating and configuring your bot
