<h1 align="center">Leo</h1>

<p align="center">
  <em>A process supervisor and task scheduler for Claude Code</em>
</p>

<p align="center">
  <a href="#install">Install</a> &middot;
  <a href="#quick-start">Quick Start</a> &middot;
  <a href="#features">Features</a> &middot;
  <a href="docs/">Documentation</a>
</p>

---

Leo manages persistent [Claude Code](https://docs.anthropic.com/en/docs/claude-code) sessions, schedules autonomous tasks, and lets you spawn on-demand coding agents. Telegram gives you mobile access. A built-in web dashboard lets you manage everything from a browser.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/blackpaw-studio/leo/refs/heads/main/install.sh | sh
```

Or with Go: `go install github.com/blackpaw-studio/leo@latest`

**Prerequisites:** [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) (authenticated), `tmux`, a [Telegram bot token](https://core.telegram.org/bots#botfather)

## Quick Start

```bash
leo setup          # interactive wizard — Telegram, profile, workspace
leo service start  # start supervised processes
```

The wizard walks you through connecting Telegram, creating a user profile, and configuring your first process. Once started, message your bot on Telegram to chat with your assistant.

Run `leo service start --daemon` to install as a system service that persists across reboots.

## Features

### Processes

Long-running Claude sessions supervised with auto-restart and exponential backoff. Each process gets its own workspace, model, channels, and permissions. Connect Telegram for mobile access, enable remote control for claude.ai/code.

```yaml
processes:
  assistant:
    channels: [plugin:telegram@claude-plugins-official]
    remote_control: true
    enabled: true
```

### Agent Templates

Define reusable blueprints for spawning ephemeral coding agents. Dispatch them from Telegram (`/agent coding owner/repo`) or the web UI. Agents clone the repo, run in their own tmux session, and show up in claude.ai with a named session.

```yaml
templates:
  coding:
    model: sonnet
    remote_control: true
    permission_mode: bypassPermissions
    workspace: ~/agents
```

### Scheduled Tasks

Cron-driven tasks that invoke Claude in non-interactive mode. Write a prompt file, set a schedule, and Leo handles the rest. Tasks can send Telegram messages, run silently, retry on failure, and route to forum topics.

```yaml
tasks:
  daily-briefing:
    schedule: "0 7 * * *"
    timezone: America/New_York
    prompt_file: prompts/daily-briefing.md
    model: opus
    enabled: true
```

### Web Dashboard

Monitor processes, manage tasks, edit config, spawn agents, and preview cron schedules from a browser on your LAN.

```yaml
web:
  enabled: true
  port: 8370
```

### Telegram Commands

| Command | Description |
|---|---|
| `/agent [template] [repo]` | Spawn a coding agent |
| `/agents` | List running agents with stop buttons |
| `/tasks` | List tasks with run/toggle buttons |
| `/stop` | Interrupt the current process |
| `/clear` | Clear conversation context |
| `/compact` | Compact conversation context |

The Telegram plugin also gives Claude tools for replies, reactions, message editing, scheduled messages, persistent memory, and more. See the [Telegram guide](docs/guides/telegram-commands.md).

## CLI

| Command | Description |
|---|---|
| `leo setup` | Interactive setup wizard |
| `leo service start` | Start supervised processes |
| `leo service stop` | Stop service |
| `leo service restart` | Restart service |
| `leo service logs` | Tail service logs |
| `leo run <task>` | Run a task once |
| `leo task list` | List tasks |
| `leo task add` | Add a task interactively |
| `leo process list` | Show process states |
| `leo status` | Overall status |
| `leo validate` | Check config and prerequisites |
| `leo config show` | Display effective config |
| `leo config edit` | Edit config interactively |
| `leo update` | Self-update binary |

See the [CLI reference](docs/cli/) for full details.

## Documentation

- [Getting Started](docs/getting-started/) &mdash; installation, prerequisites, first run
- [Configuration](docs/configuration/) &mdash; full config reference and workspace structure
- [CLI Reference](docs/cli/) &mdash; every command and flag
- [Guides](docs/guides/) &mdash; Telegram setup, writing tasks, agents, scheduling, background mode
- [Development](docs/development/) &mdash; contributing, architecture, releasing

## Development

```bash
make build      # Build to bin/leo
make test       # Run tests with race detector
make lint       # go vet + staticcheck
```

## License

MIT
