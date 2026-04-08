# Leo Agent Reference

You are **{{.Name}}**, managed by Leo. This file gives you baseline knowledge about your own infrastructure so you can self-diagnose, manage tasks, and maintain your workspace.

## Quick Facts

- **Workspace**: `{{.Workspace}}`
- **Config**: `{{.Workspace}}/leo.yaml`
- **Agent persona**: `~/.claude/agents/{{.Name}}.md`
- **Leo binary**: run `leo` commands via Bash tool

## Telegram Messaging Rules (MANDATORY — read before every reply)

Always use the Telegram plugin's `reply` tool for all messages (DMs and group/forum chats).

### Group/Forum chats (negative `chat_id`)

For group/forum chats, you **must** pass the `thread_id` parameter to route messages to the correct topic.

#### Finding the correct `thread_id`

1. **Best**: use the `thread_id` or `message_thread_id` from the inbound `<channel>` tag if present
2. **Fallback**: read the topic cache seeded at chat startup:
   ```bash
   cat {{.Workspace}}/state/topics.json
   # Returns: [{"id":3,"name":"Chat"},{"id":7,"name":"Alerts"}]
   ```
   Pick the topic matching the context of the conversation. If the file is missing or empty, ask the user to restart the chat session after sending a message in each topic.

### Common mistakes to avoid

- **DO NOT** omit `thread_id` for group/forum chats — the message will land in the wrong topic
- **DO NOT** use `reply_to` thinking it routes to a topic — it creates a visible quote-reply instead

## What is Leo?

Leo is the CLI that scaffolded this workspace and manages your lifecycle:
- **`leo chat`** starts your interactive Telegram session
- **`leo run <task>`** executes a scheduled task (invoked by cron)
- **`leo task`** manages task definitions in leo.yaml
- **`leo cron`** installs/removes system crontab entries
- **`leo validate`** checks config and prerequisites

You are not Leo. Leo is the management layer; you are the agent it manages.

## Workspace Layout

```
{{.Workspace}}/
├── leo.yaml           # Config (tasks, telegram, defaults)
├── CLAUDE.md          # This file
├── USER.md            # Who you work for
├── HEARTBEAT.md       # Heartbeat task checklist
├── daily/             # Daily observation logs (YYYY-MM-DD.md)
├── reports/           # Task prompt files
├── state/             # Logs and runtime state
│   ├── chat.log       # Chat session output
│   ├── chat.pid       # Background process PID
│   └── <task>.log     # Per-task execution logs
├── config/
│   └── mcp-servers.json
├── scripts/           # Helper scripts
└── skills/            # Operational guides (see below)
```

## Skills (Detailed Guides)

Read these on demand when you need to perform specific operations:

- `skills/managing-tasks.md` — Add, remove, enable, disable tasks; cron schedules
- `skills/debugging-logs.md` — Log locations, reading output, common failures
- `skills/daemon-management.md` — Chat start/stop/restart/status, launchd/systemd
- `skills/config-reference.md` — Full leo.yaml field reference
- `skills/workspace-maintenance.md` — Daily logs, MCP config, workspace hygiene

## Common Operations

### Check status
```bash
leo chat status              # Chat daemon running?
leo chat status --daemon     # OS service status
leo task list                # Configured tasks
leo cron list                # Installed cron entries
leo validate                 # Config and prereq health check
```

### Read recent logs
```bash
tail -50 {{.Workspace}}/state/chat.log
tail -50 {{.Workspace}}/state/<task>.log
```

### Manage tasks
```bash
leo task add                 # Interactive task creation
leo task enable <name>       # Enable a disabled task
leo task disable <name>      # Disable without removing
leo task remove <name>       # Remove from config
leo cron install             # Sync crontab after changes
```

### Run a task manually
```bash
leo run <task>               # Execute now
leo run <task> --dry-run     # Show assembled prompt only
```
