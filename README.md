<h1 align="center">Leo</h1>

<p align="center">
  <em>Claude Code agents as persistent, proactive personal assistants</em>
</p>

<p align="center">
  <a href="#install">Install</a> &middot;
  <a href="#quick-start">Quick Start</a> &middot;
  <a href="#how-it-works">How It Works</a> &middot;
  <a href="#cli-reference">CLI Reference</a> &middot;
  <a href="#configuration">Configuration</a>
</p>

---

Leo is a CLI tool that sets up and manages a single [Claude Code](https://docs.anthropic.com/en/docs/claude-code) agent as a persistent, mobile-accessible personal assistant. It handles workspace scaffolding, persistent memory, Telegram integration, and cron scheduling — giving your agent a personality, continuity across sessions, and the ability to work on a schedule or respond to messages from your phone.

Leo is **not** a multi-agent orchestration framework, and it is **not** a direct replacement for [OpenClaw](https://github.com/openclaw). While Leo includes a migration path for existing OpenClaw users (`leo migrate`), it is a simpler, more focused tool: one agent, one workspace, one config file. Leo manages the config, prompt assembly, and cron entries — your system's cron runs `claude` directly.

## Install

**Homebrew**

```bash
brew install blackpaw-studio/tap/leo
```

**Go**

```bash
go install github.com/blackpaw-studio/leo@latest
```

**From source**

```bash
git clone https://github.com/blackpaw-studio/leo.git
cd leo
make install
```

### Prerequisites

- [Claude Code CLI](https://docs.anthropic.com/en/docs/claude-code) — installed and authenticated
- `curl` — used by agents for outbound Telegram messages
- A [Telegram bot token](https://core.telegram.org/bots#botfather) (the setup wizard walks you through this)

## Quick Start

```bash
leo setup
```

The interactive wizard will guide you through:

1. Naming your agent
2. Choosing a workspace directory
3. Picking a personality template (chief-of-staff, dev-assistant, or skeleton)
4. Creating a user profile
5. Connecting Telegram (bot token + chat ID auto-detection)
6. Configuring MCP servers (optional)
7. Adding scheduled tasks
8. Installing cron entries
9. Running a test message

Once setup is complete, start an interactive Telegram session:

```bash
leo chat
```

Or run a scheduled task manually to verify it works:

```bash
leo run heartbeat
```

## How It Works

Leo operates in two modes, both invoking the stock `claude` CLI:

### Interactive Mode

`leo chat` starts a long-running Claude session with the official Telegram channel plugin. Messages flow through Telegram in both directions — the user sends messages to the bot, and the agent replies through the channel plugin.

```
User (Telegram) ──> Telegram Bot API ──> claude (channel plugin) ──> Agent
                                                                      │
User (Telegram) <── Telegram Bot API <── claude (channel plugin) <────┘
```

### Scheduled Tasks

System cron calls `leo run <task>`, which reads the config, assembles a prompt, and invokes `claude -p` in non-interactive mode. If the agent has something to report, it sends a Telegram message via `curl` (the notification protocol is injected into the prompt at runtime).

```
cron ──> leo run <task> ──> claude -p "<assembled prompt>" ──> Agent
                                                                 │
                          User (Telegram) <── curl Bot API <─────┘
```

Tasks can run silently — if there's nothing to report, the agent outputs `NO_REPLY` and exits.

### Running in the Background

For production use, you'll want the Telegram session to stay alive and automatically restart if it crashes. Leo provides two options:

**Simple background mode** — spawns a supervised process with automatic restart and exponential backoff. No OS-level daemon installation required.

```bash
leo chat start            # start in background with auto-restart
leo chat status           # check if running
leo chat stop             # stop the background session
```

**Daemon mode** — installs a launchd plist (macOS) or systemd user unit (Linux) for OS-level supervision that persists across reboots.

```bash
leo chat start --daemon   # install and start as OS service
leo chat status --daemon  # check daemon status
leo chat stop --daemon    # uninstall OS service
```

Logs for both modes are written to `<workspace>/state/chat.log`.

## CLI Reference

| Command | Description |
|---|---|
| `leo setup` | Interactive setup wizard for a new agent |
| `leo chat` | Start an interactive Telegram session (foreground) |
| `leo chat start` | Start chat in background with auto-restart |
| `leo chat stop` | Stop background chat session |
| `leo chat status` | Show chat session status |
| `leo run <task>` | Run a scheduled task once (cron entry point) |
| `leo cron install` | Install all enabled tasks to system crontab |
| `leo cron remove` | Remove all Leo-managed cron entries |
| `leo cron list` | Show installed schedules |
| `leo task list` | List configured tasks |
| `leo task add` | Add a new scheduled task interactively |
| `leo task enable <name>` | Enable a task |
| `leo task disable <name>` | Disable a task |
| `leo migrate` | Migrate from an existing OpenClaw installation |
| `leo version` | Print version |

The `start`, `stop`, and `status` subcommands accept a `--daemon` flag to use OS-level service management (launchd/systemd) instead of a simple background process.

### Global Flags

```
-c, --config <path>       Path to leo.yaml (default: auto-detect by walking up from cwd)
-w, --workspace <path>    Workspace directory (default: from config)
```

## Configuration

Leo is configured via a single `leo.yaml` file in your workspace directory.

```yaml
agent:
  name: leo
  workspace: ~/leo
  agent_file: ~/.claude/agents/leo.md    # optional, defaults to ~/.claude/agents/<name>.md

telegram:
  bot_token: "YOUR_BOT_TOKEN"
  chat_id: "YOUR_CHAT_ID"
  group_id: "-100XXXXXXXXXX"                # optional: forum group
  topics:                                    # optional: forum topic IDs
    alerts: 1
    news: 3

defaults:
  model: sonnet
  max_turns: 15

tasks:
  heartbeat:
    schedule: "0,30 7-22 * * *"
    timezone: America/New_York
    prompt_file: HEARTBEAT.md               # relative to workspace
    model: sonnet                            # overrides defaults.model
    max_turns: 10
    topic: alerts                            # routes to telegram.topics.alerts
    enabled: true

  daily-news-briefing:
    schedule: "0 7 * * *"
    timezone: America/New_York
    prompt_file: reports/daily-news-briefing.md
    model: opus
    max_turns: 20
    topic: news
    enabled: true
    silent: true                             # agent works silently, only sends final message
```

### Task Options

| Field | Description | Default |
|---|---|---|
| `schedule` | Cron expression | *required* |
| `timezone` | IANA timezone | — |
| `prompt_file` | Path to prompt (relative to workspace) | *required* |
| `model` | Claude model override | `defaults.model` |
| `max_turns` | Max agent turns override | `defaults.max_turns` |
| `topic` | Telegram topic key (maps to `telegram.topics`) | — |
| `enabled` | Whether cron should run this task | `false` |
| `silent` | Prepend silent-mode preamble to prompt | `false` |

## Workspace Structure

```
~/leo/
├── leo.yaml                    # Leo config
├── USER.md                     # Your profile (created during setup)
├── HEARTBEAT.md                # Heartbeat checklist prompt
├── MEMORY.md                   # Symlink → ~/.claude/agent-memory/<name>/MEMORY.md
├── daily/                      # Raw daily logs
├── reports/                    # Task prompt files
├── state/                      # Runtime logs
├── config/
│   └── mcp-servers.json        # MCP server configuration
└── scripts/                    # Helper scripts
```

## Agent Templates

Leo ships with three agent personality templates, selected during setup:

| Template | Description |
|---|---|
| **chief-of-staff** | Proactive executive assistant — triages messages, manages calendar, sends briefings |
| **dev-assistant** | Development-focused agent — monitors repos, runs checks, surfaces issues |
| **skeleton** | Minimal starting point — bring your own personality and instructions |

Templates are rendered into standard [Claude Code custom agents](https://docs.anthropic.com/en/docs/claude-code) at `~/.claude/agents/<name>.md` with `memory: user` enabled for persistent memory across sessions.

## What Leo Is (and Isn't)

**Leo is** a setup and management tool for a single Claude Code agent. It gives your agent:

- A **personality** via agent templates (chief-of-staff, dev-assistant, or custom)
- **Persistent memory** across sessions via `MEMORY.md` and Claude Code's `memory: user` feature
- **Mobile access** via Telegram — chat with your agent from your phone
- **Scheduled tasks** via cron — your agent can check in, send briefings, and run background work autonomously

**Leo is not:**

- A multi-agent orchestration framework — it manages a single agent
- A replacement for the Claude API or Agent SDK — it wraps the stock `claude` CLI
- A daemon or long-running service (except during `leo chat`) — cron runs `claude` directly
- A direct replacement for [OpenClaw](https://github.com/openclaw) — Leo is simpler and more focused, though it includes `leo migrate` for OpenClaw users who want to transition

## Migrating from OpenClaw

If you have an existing OpenClaw installation, Leo can import your workspace, agent files, cron jobs, and Telegram config:

```bash
leo migrate
```

See `leo migrate --help` for details.

## Development

```bash
make build      # Build to bin/leo
make test       # Run tests with race detector
make lint       # go vet + staticcheck
```

## License

MIT
