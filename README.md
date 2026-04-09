<h1 align="center">Leo</h1>

<p align="center">
  <em>A process supervisor and task scheduler for Claude Code</em>
</p>

<p align="center">
  <a href="#install">Install</a> &middot;
  <a href="#quick-start">Quick Start</a> &middot;
  <a href="#how-it-works">How It Works</a> &middot;
  <a href="#cli-reference">CLI Reference</a> &middot;
  <a href="#configuration">Configuration</a>
</p>

---

Leo is a CLI tool that supervises persistent [Claude Code](https://docs.anthropic.com/en/docs/claude-code) processes and schedules tasks. It manages multiple Claude processes — each with its own workspace, model, and channel configuration — along with cron-driven tasks for autonomous background work. Telegram integration gives you mobile access to your assistants, and scheduled tasks let them check in, send briefings, and work on a schedule.

Leo manages the config, prompt assembly, and cron entries — your system's cron runs `claude` directly.

## Install

**Install script** (macOS and Linux)

```bash
curl -fsSL https://raw.githubusercontent.com/blackpaw-studio/leo/refs/heads/main/install.sh | sh
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

1. Creating a user profile
2. Connecting Telegram (bot token + chat ID auto-detection)
3. Configuring MCP servers (optional)
4. Adding scheduled tasks
5. Installing cron entries
6. Running a test message

Once setup is complete, start the service:

```bash
leo service start
```

Or run a scheduled task manually to verify it works:

```bash
leo run heartbeat
```

## How It Works

Leo operates in two modes, both invoking the stock `claude` CLI:

### Processes (Interactive Mode)

`leo service start` launches one or more persistent Claude sessions defined in the `processes:` config section. Each process can have its own workspace, model, channels (like Telegram), and additional directories. The service supervises all enabled processes, restarting them on failure.

```
User (Telegram) ──> Telegram Bot API ──> claude (channel plugin) ──> Process
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

For production use, you'll want the service to stay alive and automatically restart if it crashes. Leo provides two options:

**Simple background mode** — spawns a supervised process with automatic restart and exponential backoff. No OS-level daemon installation required.

```bash
leo service start            # start in background with auto-restart
leo service status           # check if running
leo service stop             # stop the background session
```

**Daemon mode** — installs a launchd plist (macOS) or systemd user unit (Linux) for OS-level supervision that persists across reboots.

```bash
leo service start --daemon   # install and start as OS service
leo service status --daemon  # check daemon status
leo service stop --daemon    # uninstall OS service
```

Logs for both modes are written to `~/.leo/state/service.log`.

## CLI Reference

| Command | Description |
|---|---|
| `leo setup` | Interactive setup wizard |
| `leo onboard` | Guided first-time setup (prerequisites + setup wizard) |
| `leo service start` | Start service in background with auto-restart |
| `leo service stop` | Stop background service |
| `leo service status` | Show service status |
| `leo service restart` | Restart background service |
| `leo service logs` | Tail service logs (`-n/--tail`, `-f/--follow`) |
| `leo run <task>` | Run a scheduled task once (cron entry point) |
| `leo cron install` | Install all enabled tasks to system crontab |
| `leo cron remove` | Remove all Leo-managed cron entries |
| `leo cron list` | Show installed schedules |
| `leo task list` | List configured tasks |
| `leo task add` | Add a new scheduled task interactively |
| `leo task remove <name>` | Remove a task from the config |
| `leo task enable <name>` | Enable a task |
| `leo task disable <name>` | Disable a task |
| `leo telegram topics` | Discover forum topics from recent messages |
| `leo session list` | List stored sessions |
| `leo session clear` | Clear stored session(s) |
| `leo status` | Show overall leo status (service, processes, tasks) |
| `leo validate` | Check config, prerequisites, and workspace health |
| `leo config show` | Display effective config with defaults applied |
| `leo update` | Update leo binary and refresh workspace files |
| `leo completion` | Generate shell completion script (bash/zsh/fish) |
| `leo version` | Print version |

The `start`, `stop`, `status`, and `restart` subcommands of `leo service` accept a `--daemon` flag to use OS-level service management (launchd/systemd) instead of a simple background process.

### Global Flags

```
-c, --config <path>       Path to leo.yaml (default: auto-detect)
```

## Configuration

Leo is configured via a single `leo.yaml` file. The default location is `~/.leo/leo.yaml`.

```yaml
telegram:
  bot_token: "YOUR_BOT_TOKEN"
  chat_id: "YOUR_CHAT_ID"
  group_id: "-100XXXXXXXXXX"                # optional: forum group

defaults:
  model: sonnet
  max_turns: 15
  bypass_permissions: false
  remote_control: false

processes:
  assistant:
    # workspace omitted -> defaults to ~/.leo/workspace
    channels:
      - plugin:telegram@claude-plugins-official
    remote_control: true
    enabled: true

  researcher:
    workspace: ~/research-agent
    model: opus
    add_dirs:
      - ~/projects/data
    enabled: true

tasks:
  heartbeat:
    schedule: "0,30 7-22 * * *"
    timezone: America/New_York
    prompt_file: HEARTBEAT.md
    enabled: true
    silent: true

  daily-news-briefing:
    schedule: "0 7 * * *"
    timezone: America/New_York
    prompt_file: reports/daily-news-briefing.md
    model: opus
    max_turns: 20
    topic_id: 3
    enabled: true
    silent: true
```

### Process Options

Each entry under `processes:` defines a persistent Claude session that the service supervises.

| Field | Description | Default |
|---|---|---|
| `workspace` | Working directory for this process | `~/.leo/workspace` |
| `channels` | Channel plugins to attach (e.g. Telegram) | — |
| `model` | Claude model override | `defaults.model` |
| `max_turns` | Max agent turns override | `defaults.max_turns` |
| `bypass_permissions` | Pass `--dangerously-skip-permissions` to claude | `defaults.bypass_permissions` |
| `remote_control` | Enable `--remote-control` for web/mobile access via claude.ai/code | `defaults.remote_control` |
| `mcp_config` | Path to MCP config (relative to workspace, or absolute) | `<workspace>/config/mcp-servers.json` |
| `add_dirs` | Additional directories to pass to claude | — |
| `enabled` | Whether the service should start this process | `false` |

### Task Options

| Field | Description | Default |
|---|---|---|
| `schedule` | Cron expression | *required* |
| `timezone` | IANA timezone | — |
| `prompt_file` | Path to prompt (relative to workspace) | *required* |
| `workspace` | Working directory for this task | `~/.leo/workspace` |
| `model` | Claude model override | `defaults.model` |
| `max_turns` | Max agent turns override | `defaults.max_turns` |
| `topic_id` | Telegram forum topic ID (discover via `leo telegram topics`) | — |
| `enabled` | Whether cron should run this task | `false` |
| `silent` | Prepend silent-mode preamble to prompt | `false` |

## Directory Structure

```
~/.leo/                          # Leo home
├── leo.yaml                     # Config
├── state/
│   ├── sessions.json            # Session state
│   ├── service.log              # Service logs
│   └── leo.sock                 # Daemon socket
└── workspace/                   # Default workspace
    ├── CLAUDE.md                # Agent instructions
    ├── USER.md                  # Your profile
    ├── HEARTBEAT.md             # Heartbeat checklist prompt
    ├── config/
    │   └── mcp-servers.json     # MCP server configuration
    ├── reports/                 # Task prompt files
    └── skills/                  # Agent skills
```

Processes can use the default workspace or specify their own via the `workspace` field.

## What Leo Is (and Isn't)

**Leo is** a process supervisor and task scheduler for Claude Code. It gives your assistants:

- **Multi-process management** — run multiple Claude sessions with independent workspaces, models, and channels
- **Persistent memory** via user-configured MCP memory servers
- **Mobile access** via Telegram — chat with your assistants from your phone
- **Remote control** via claude.ai/code — access your assistants from any browser
- **Scheduled tasks** via cron — your assistants can check in, send briefings, and run background work autonomously

**Leo is not:**

- A replacement for the Claude API or Agent SDK — it wraps the stock `claude` CLI
- A daemon or long-running service (except during `leo service`) — cron runs `claude` directly

## Development

```bash
make build      # Build to bin/leo
make test       # Run tests with race detector
make lint       # go vet + staticcheck
```

## License

MIT
