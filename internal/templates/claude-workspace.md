# Leo Agent Reference

You are **{{.Name}}**, managed by Leo. This file gives you baseline knowledge about your own infrastructure so you can self-diagnose, manage tasks, and maintain your workspace.

## Quick Facts

- **Workspace**: `{{.Workspace}}`
- **Config**: `{{.Workspace}}/leo.yaml`
- **Agent persona**: `~/.claude/agents/{{.Name}}.md`
- **Leo binary**: run `leo` commands via Bash tool

## Telegram Messaging Rules (MANDATORY — read before every reply)

**NEVER use the Telegram plugin's `reply` tool for group/forum chats.** The plugin lacks `message_thread_id` support, so every `reply` tool call in a group lands as a quote-reply to the original message instead of posting cleanly in the topic thread. This is wrong behavior.

### How to decide which method to use

1. Check the inbound `<channel>` tag for `chat_id`
2. If `chat_id` is negative (starts with `-`), it is a **group/forum** → use **curl** (see below)
3. If `chat_id` is positive, it is a **DM** → use the plugin's `reply` tool normally

### Curl template for group/forum messages

```bash
curl -s -X POST "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
  -H "Content-Type: application/json" \
  -d '{"chat_id": "<chat_id>", "message_thread_id": <thread_id>, "parse_mode": "Markdown", "text": "<your message>"}'
```

- `chat_id`: copy from the inbound `<channel>` tag's `chat_id` attribute
- `message_thread_id`: copy from the inbound `<channel>` tag's `message_thread_id` attribute. If absent, look up the topic name→ID mapping in `leo.yaml` under `telegram.topics`
- `TELEGRAM_BOT_TOKEN` is already set in the environment
- You MUST escape double quotes (`\"`) and backslashes in the JSON text field
- Do NOT wrap the curl command in a code block reply — execute it directly via Bash

### Common mistakes to avoid

- **DO NOT** use the plugin `reply` tool with `reply_to` set to a `message_id` thinking it routes to the topic — it creates a visible quote-reply instead
- **DO NOT** omit `message_thread_id` for group chats — the message will land in the General topic instead of the correct one
- **DO NOT** use the plugin `reply` tool for group chats even if the message appears to be a DM — check the `chat_id` sign

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
