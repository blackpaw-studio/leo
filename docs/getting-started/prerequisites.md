# Prerequisites

Leo requires a few tools to be installed and configured before it can manage your agents.

## Claude Code CLI

Leo invokes the `claude` CLI to run agents. You need it installed and authenticated.

**Install:**

```bash
npm install -g @anthropic-ai/claude-code
```

**Authenticate:**

```bash
claude auth
```

See the [Claude Code documentation](https://docs.anthropic.com/en/docs/claude-code) for full setup instructions.

!!! warning "Authentication required"
    Leo will not work if `claude` is not authenticated. `leo setup` and `leo validate` both check for this automatically.

## curl

Used by agents to send outbound Telegram messages during scheduled tasks. Pre-installed on macOS and most Linux distributions.

```bash
curl --version
```

## Telegram Bot Token

You need a Telegram bot to receive messages from your agent. The `leo setup` wizard walks you through this, but if you want to prepare ahead of time:

1. Open Telegram and message [@BotFather](https://t.me/BotFather)
2. Send `/newbot` and follow the prompts
3. Copy the bot token you receive

See the [Telegram Setup](../guides/telegram-setup.md) guide for a detailed walkthrough.

## Supported Platforms

| Platform | Scheduled Tasks | Interactive Chat | Daemon Mode |
|----------|----------------|-----------------|-------------|
| macOS | in-process scheduler | Yes | launchd |
| Linux | in-process scheduler | Yes | systemd (user) |

## Optional: MCP Servers

Leo can pass MCP server configuration to Claude for integrations like Google Calendar, Gmail, and other services. This is configured during setup or by editing `config/mcp-servers.json` in your workspace.
