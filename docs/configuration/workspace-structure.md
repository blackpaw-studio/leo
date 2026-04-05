# Workspace Structure

Leo creates a workspace directory during setup that holds all config, prompts, logs, and state for your agent.

## Directory Layout

```
~/leo/
├── leo.yaml                    # Leo config
├── USER.md                     # Your profile (created during setup)
├── HEARTBEAT.md                # Heartbeat checklist prompt
├── MEMORY.md                   # Symlink -> ~/.claude/agent-memory/<name>/MEMORY.md
├── daily/                      # Raw daily observation logs
├── reports/                    # Task prompt files
├── state/                      # Runtime state and logs
│   ├── chat.log                # Interactive session log
│   ├── chat.pid                # Background process PID file
│   └── <task>.log              # Per-task execution logs
├── config/
│   └── mcp-servers.json        # MCP server configuration
└── scripts/                    # Helper scripts (optional)
```

## Key Files

### `leo.yaml`

The main configuration file. See [Configuration](index.md) for details.

### `USER.md`

A user profile filled in during setup. This is included in the agent's context so it knows who you are, your role, preferences, and timezone.

### `HEARTBEAT.md`

A checklist template used by the default heartbeat task. It tells the agent what to check (unread messages, calendar, pending tasks, alerts) and how to format the output.

### `MEMORY.md`

A symlink to `~/.claude/agent-memory/<name>/MEMORY.md`. This is Claude Code's persistent memory system — the agent reads and writes to this file across sessions to maintain continuity.

### `daily/`

The agent writes daily observation logs here as `YYYY-MM-DD.md` files. These accumulate context over time.

### `reports/`

Store your task prompt files here. Each task in `leo.yaml` references a `prompt_file` relative to the workspace, e.g., `reports/daily-news-briefing.md`.

### `state/`

Runtime files managed by Leo:

- **`chat.log`** — output from the interactive Telegram session
- **`chat.pid`** — PID file for background chat mode (`leo chat start`)
- **`<task>.log`** — stdout/stderr from each task execution, useful for debugging

### `config/mcp-servers.json`

MCP server configuration passed to Claude via `--mcp-config`. Configure integrations like Google Calendar, Gmail, or custom tools here.

### `scripts/`

An optional directory for helper scripts your agent or tasks might use.
