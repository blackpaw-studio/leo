# Leo

A CLI tool that sets up and manages Claude Code agents as persistent, proactive personal assistants. Handles workspace setup, Telegram integration, and cron/launchd scheduling. Optionally migrates from OpenClaw.

After setup, system cron runs `claude` directly. Leo manages the config and cron entries.

Written in Go. Single binary, no runtime dependencies.

🐈‍⬛

## How It Works

Two runtime modes, both stock `claude` CLI:

1. **Interactive (inbound Telegram):** User runs `leo chat`, which starts:
   ```bash
   claude --agent <name> --channels plugin:telegram@claude-plugins-official \
     --mcp-config <workspace>/config/mcp-servers.json --add-dir <workspace>
   ```
   Long-running session. User messages via Telegram, agent replies via the channel plugin.

2. **Scheduled tasks:** System cron (managed by Leo) runs:
   ```bash
   leo run <task>
   ```
   Which reads config, assembles the prompt, and executes:
   ```bash
   cd <workspace> && claude --agent <name> -p "<assembled prompt>" \
     --model <model> --max-turns <max_turns> \
     --mcp-config <workspace>/config/mcp-servers.json --add-dir <workspace> \
     --dangerously-skip-permissions --output-format text
   ```
   Agent does its work and sends Telegram alerts via `curl` to the Bot API if needed.

Both share the same agent file (`~/.claude/agents/<name>.md`) and persistent memory (`~/.claude/agent-memory/<name>/MEMORY.md`).

### Why curl for outbound Telegram?

The official Telegram channel plugin only supports replying to inbound messages (reply, react, edit_message). There is no `send_message` tool for proactive outbound. So scheduled tasks include a curl template in the prompt. The agent calls `curl` via Bash when it needs to alert the user.

## CLI

```bash
leo setup                  # Interactive setup wizard (fresh install)
leo migrate                # Migrate from OpenClaw
leo chat                   # Start interactive Telegram session
leo run <task>             # Run a scheduled task once (testing or cron entry point)
leo cron install           # Install all enabled tasks to system crontab / launchd
leo cron remove            # Remove all leo-managed cron entries
leo cron list              # Show installed schedules
leo task list              # List configured tasks from leo.yaml
leo task add               # Add a new scheduled task interactively
leo task enable <name>     # Enable a task
leo task disable <name>    # Disable a task
leo version                # Print version
```

## Config

`<workspace>/leo.yaml`:

```yaml
agent:
  name: rocket
  workspace: /Volumes/evan/rocket
  # Path to agent .md file (default: ~/.claude/agents/<name>.md)
  agent_file: ~/.claude/agents/rocket.md

telegram:
  bot_token: "8692297606:AAFUQo0..."
  chat_id: "8076794787"
  # Optional: forum group with topics
  group_id: "-1003898158900"
  topics:
    alerts: 1
    construction: 2
    news: 3

defaults:
  model: sonnet
  max_turns: 15

tasks:
  heartbeat:
    schedule: "0,30 7-22 * * *"
    timezone: America/New_York
    prompt_file: HEARTBEAT.md       # Relative to workspace
    model: sonnet                   # Override default
    max_turns: 10
    topic: alerts                   # Telegram topic for delivery
    enabled: true

  inbox-calendar-watch:
    schedule: "0,30 7-21 * * *"
    timezone: America/New_York
    prompt_file: reports/inbox-calendar-watch.md
    max_turns: 15
    topic: alerts
    enabled: true
    silent: true                    # Prepend "SILENT SCHEDULED RUN" preamble

  daily-news-briefing:
    schedule: "0 7 * * *"
    timezone: America/New_York
    prompt_file: reports/daily-news-briefing.md
    model: opus                     # Use opus for this task
    max_turns: 20
    topic: news
    enabled: true
    silent: true
```

### Config Details

- `prompt_file` is read at runtime by `leo run`. Paths are relative to workspace.
- `model` per task overrides `defaults.model`.
- `topic` maps to the key in `telegram.topics` to determine `message_thread_id`.
- `silent: true` prepends a preamble to the prompt telling the agent to work silently and only emit a final message or `NO_REPLY`.
- Telegram credentials are in the config file. The file should be chmod 600.

## `leo run <task>`

This is the core runtime command. Cron entries invoke this.

1. Load `leo.yaml`
2. Look up the task config
3. Read the prompt file from workspace
4. Assemble the full prompt:
   - If `silent`, prepend silent preamble
   - Append the prompt file content
   - Append the Telegram notification protocol (curl template with bot token, chat ID, and topic routing from config)
5. Execute:
   ```bash
   cd <workspace>
   claude --agent <agent_name> \
     -p "<assembled prompt>" \
     --model <task.model || defaults.model> \
     --max-turns <task.max_turns || defaults.max_turns> \
     --mcp-config <workspace>/config/mcp-servers.json \
     --add-dir <workspace> \
     --dangerously-skip-permissions \
     --output-format text
   ```
6. Log output to `<workspace>/state/<task>.log`

### Telegram Notification Protocol

Appended to every scheduled task prompt:

```
## Telegram Notification Protocol
If anything needs the user's attention, send a Telegram message using:
  curl -s -X POST "https://api.telegram.org/bot<TOKEN>/sendMessage" \
    -d chat_id="<CHAT_ID>" \
    -d message_thread_id="<TOPIC_ID>" \
    -d parse_mode=Markdown \
    -d text="<your message>"

If nothing needs attention, reply NO_REPLY and exit.
Do not include process narration, status updates, or tool output. Only emit the final user-facing message or NO_REPLY.
```

Token, chat ID, and topic ID are injected from config at prompt assembly time.

## `leo cron install`

Reads all enabled tasks from `leo.yaml` and writes cron entries:

```cron
# === LEO:rocket — DO NOT EDIT ===
# leo:rocket:heartbeat
0,30 7-22 * * * /usr/local/bin/leo run heartbeat >> /Volumes/evan/rocket/state/heartbeat.log 2>&1
# leo:rocket:inbox-calendar-watch
0,30 7-21 * * * /usr/local/bin/leo run inbox-calendar-watch >> /Volumes/evan/rocket/state/inbox-calendar-watch.log 2>&1
# leo:rocket:daily-news-briefing
0 7 * * * /usr/local/bin/leo run daily-news-briefing >> /Volumes/evan/rocket/state/daily-news-briefing.log 2>&1
# === END LEO:rocket ===
```

Marker comments with agent name allow clean install/remove and support multiple agents.

`leo cron remove` strips the block between markers.
`leo cron list` reads crontab, shows each task with schedule and last log timestamp.

On macOS, optionally generate launchd plists instead (user choice during setup).

## Workspace Structure

```
<workspace>/
├── leo.yaml                   # Leo config
├── USER.md                    # Human's profile
├── HEARTBEAT.md               # Heartbeat checklist
├── MEMORY.md                  # Symlink → ~/.claude/agent-memory/<name>/MEMORY.md
├── daily/                     # Raw daily logs (YYYY-MM-DD.md)
├── reports/                   # Task prompt files
├── state/                     # Runtime logs and state
│   ├── heartbeat.log
│   ├── inbox-calendar-watch.log
│   └── inbox-calendar-watch.json  # Agent-managed dedup state
├── config/
│   └── mcp-servers.json       # MCP server config
└── scripts/                   # Helper scripts
```

## Agent File

`~/.claude/agents/<name>.md` — standard Claude Code custom subagent:

```yaml
---
name: <name>
description: <description>
model: opus
memory: user
tools: Read, Write, Edit, Bash, Grep, Glob, WebSearch, WebFetch
---

<merged identity, personality, style, workspace conventions,
 startup read sequence, memory protocol, tool guidelines,
 proactive behavior rules, red lines>
```

`memory: user` makes Claude Code auto-create `~/.claude/agent-memory/<name>/MEMORY.md` and inject the first 200 lines into the system prompt every session.

The agent prompt body tells the agent:
- Where the workspace lives and what files to read on startup
- How to write daily logs
- When/how to curate MEMORY.md
- Tool-specific rules (read-only access to X, confirm before Y, etc.)
- NOTE: The agent prompt does NOT include the Telegram curl protocol — that's appended dynamically by `leo run` only for scheduled tasks, so the interactive session doesn't have it cluttering the prompt.

## `leo setup`

Interactive wizard:

1. **Agent name** — used as filename and memory scope
2. **Workspace directory** — where to create the workspace (default: `~/<name>/`)
3. **Personality** — pick template (chief-of-staff, dev-assistant, skeleton) or custom ($EDITOR)
4. **User profile** — guided prompts, writes USER.md
5. **Telegram:**
   - Enter bot token (with BotFather instructions printed)
   - Auto-detect chat ID: send test message to bot, poll getUpdates API
   - Optional: group ID and topic IDs
   - Install official Telegram channel plugin if not present
6. **MCP servers** — optional, interactive config, writes `config/mcp-servers.json`
7. **Tasks** — pick from built-in tasks (heartbeat, inbox watch, news briefing) or skip
8. **Write everything:** agent file, workspace dirs, leo.yaml, symlink memory
9. **Install cron** — `leo cron install`
10. **Test** — invoke agent once + send test Telegram message

## `leo migrate`

Migrates from an existing OpenClaw installation:

1. **Find OpenClaw** — check `~/.openclaw/workspace/`, `/Volumes/*/.openclaw/workspace/`, or ask
2. **Agent name** — read from `IDENTITY.md` or ask
3. **Workspace dir** — ask (suggest same parent as OpenClaw workspace)
4. **Merge agent files:**
   - Read `SOUL.md`, `IDENTITY.md`, `AGENTS.md`, `TOOLS.md`
   - Combine into single agent `.md` with YAML frontmatter
   - Strip OpenClaw-specific instructions (heartbeat polling format, OpenClaw health checks, gateway references)
   - Add Claude Code-specific instructions (workspace path, startup read sequence, memory protocol)
5. **Copy workspace files:**
   - `USER.md`, `HEARTBEAT.md` → workspace root
   - `MEMORY.md` → `~/.claude/agent-memory/<name>/MEMORY.md` + symlink
   - `memory/*.md` → `daily/`
   - `Daily/*` → `daily/` (merge)
   - `reports/*` → `reports/`
   - `state/*` → `state/`
   - `config/*` → `config/`
   - `scripts/*` → `scripts/`
6. **Rewrite paths** — find/replace `/path/to/.openclaw/workspace/` with new workspace path in all `.md` files
7. **Parse cron jobs:**
   - Read `cron/jobs.json`
   - Convert each job's schedule, prompt, timeout, and delivery target
   - Skip OpenClaw-specific jobs (gateway health, OpenClaw updates)
   - Write tasks section in `leo.yaml`
8. **Telegram config:**
   - Check for existing `~/.claude/channels/telegram/.env`
   - Read OpenClaw `credentials/telegram-*.json` for chat IDs / group IDs
   - Populate telegram section in `leo.yaml`
9. **Install cron** — `leo cron install`
10. **Test** — invoke agent + send test Telegram message
11. **Print summary** — what was migrated, what was skipped, next steps

## Repo Structure

```
leo/
├── cmd/
│   └── leo/
│       └── main.go
├── internal/
│   ├── config/          # YAML config loading/saving
│   ├── setup/           # Setup wizard
│   ├── migrate/         # OpenClaw migration
│   ├── run/             # Task runner (prompt assembly + claude invocation)
│   ├── cron/            # Crontab / launchd management
│   ├── telegram/        # Telegram API helpers (test message, getUpdates)
│   └── templates/       # Embedded agent/heartbeat/user-profile templates
├── templates/
│   ├── agent-chief-of-staff.md
│   ├── agent-dev-assistant.md
│   ├── agent-skeleton.md
│   ├── heartbeat.md
│   └── user-profile.md
├── go.mod
├── go.sum
├── Makefile
├── README.md
├── LICENSE
└── AGENTS.md
```

Templates embedded via `embed.FS`.

## Dependencies

Runtime: `claude` CLI (authenticated), `curl` (for Telegram from agent prompts)
Build: Go 1.22+

## Distribution

- `go install github.com/blackpaw-studio/leo@latest`
- Homebrew tap
- GitHub releases via goreleaser (darwin-arm64, darwin-amd64, linux-arm64, linux-amd64)
