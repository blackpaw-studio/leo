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

## tmux

Required for supervised processes and ephemeral agents. Leo wraps each Claude session in a tmux session so channel plugins that need a terminal can attach to one.

```bash
tmux -V
```

On macOS: `brew install tmux`. On Linux: your distro's package manager (`apt install tmux`, `dnf install tmux`, etc.).

## Optional: Channel Plugin

Leo does not ship with any built-in messaging channel. To surface messages via Telegram, Slack, webhook, or another channel, install the corresponding Claude Code plugin separately. For example:

```bash
claude plugin install telegram@claude-plugins-official
```

Then reference the plugin ID in your process or task `channels:` list. See [Configuration → Channels](../configuration/config-reference.md#channels).

## Supported Platforms

| Platform | Scheduled Tasks | Interactive Chat | Daemon Mode |
|----------|----------------|-----------------|-------------|
| macOS | in-process scheduler | Yes | launchd |
| Linux | in-process scheduler | Yes | systemd (user) |

## Optional: MCP Servers

Leo can pass MCP server configuration to Claude for integrations like Google Calendar, Gmail, and other services. This is configured during setup or by editing `config/mcp-servers.json` in your workspace.
