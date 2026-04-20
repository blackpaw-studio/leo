# leo channels

Per-channel-type bootstrap helpers.

Channel plugins themselves are installed and configured via Claude Code's plugin system (`claude plugin install telegram@claude-plugins-official`, etc.). These commands cover the small set of bootstrap concerns Leo handles generically — for example, registering the universal slash-command list with each channel's API so native autocomplete menus (Telegram's BotFather-style `/`-menu, Discord's slash-command picker) include Leo's commands.

## leo channels register-commands

Register Leo's universal slash commands with a channel's API.

### Usage

```bash
leo channels register-commands <type>
```

### Description

Publishes Leo's universal command set — `/clear`, `/compact`, `/stop`, `/tasks`, `/agent`, `/agents` — to the channel's API so users see them in the native autocomplete menu.

The operation is **idempotent**: re-running replaces the published list (Telegram's `setMyCommands` is replace-not-merge). Leo fetches the existing command list at each scope first and merges its commands on top, so non-Leo commands registered by the channel plugin are preserved.

### Supported channel types

| Type | Notes |
|------|-------|
| `telegram` | Registers commands at the `default`, `all_private_chats`, and `all_group_chats` scopes. |

### Telegram bot token resolution

Leo looks for `TELEGRAM_BOT_TOKEN` in this order:

1. Environment variable `TELEGRAM_BOT_TOKEN`
2. `~/.claude/channels/blackpaw-telegram/.env`
3. `~/.claude/channels/telegram/.env`

The plugin's own `.env` is the usual source — you rarely need to export the variable yourself.

### Example

```bash
leo channels register-commands telegram
```

Expected output:

```
  scope default: 6 commands
  scope all_private_chats: 6 commands
  scope all_group_chats: 6 commands
Registered Leo commands with Telegram (token from /Users/me/.claude/channels/blackpaw-telegram/.env).
```

## See Also

- [Configuration → Channels](../configuration/config-reference.md#channels) — wiring up a channel plugin in `leo.yaml`
