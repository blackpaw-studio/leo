# Leo Agent Reference

You are managed by Leo. This file gives you baseline knowledge about your own infrastructure so you can self-diagnose, manage tasks, and maintain your workspace.

## Quick Facts

- **Workspace**: `{{.Workspace}}`
- **Config**: `{{.Workspace}}/leo.yaml`
- **Leo binary**: run `leo` commands via Bash tool

## Channel Messaging Rules (MANDATORY — read before every reply)

Leo does not ship with any specific channel. Channels are Claude Code plugins the user installs separately (e.g. Telegram, Slack, Discord, webhook). Your configured channels for this process are exposed via the `LEO_CHANNELS` environment variable as a comma-separated list of plugin IDs like `plugin:telegram@claude-plugins-official`.

Rules for replies:

1. If an installed channel plugin provides a messaging tool (for example `reply`, `sendMessage`, `notify`), **use it** for all user-facing output. Do not narrate to stdout.
2. If `LEO_CHANNELS` is empty or no channel tool is available, output `NO_REPLY` and exit. Do not print progress narration — nobody will see it.
3. When a channel plugin exposes per-channel routing hints (thread IDs, topic IDs, room IDs) via an inbound `<channel>` tag or equivalent, honor them. Consult the plugin's own docs for the exact parameter names and fallbacks.

The plugin owns its own config, auth, and routing. Leo does not manage channel credentials — only the list of plugin IDs this process should use.

## What is Leo?

Leo is the CLI that scaffolded this workspace and manages your lifecycle:
- **`leo service start`** starts supervised processes
- **`leo service start --daemon`** installs as a persistent system service
- **`leo run <task>`** executes a scheduled task
- **`leo task`** manages task definitions in leo.yaml
- **`leo validate`** checks config and prerequisites

You are not Leo. Leo is the management layer; you are the agent it manages.

## Workspace Layout

```
{{.Workspace}}/
├── leo.yaml           # Config (tasks, defaults, processes, templates)
├── CLAUDE.md          # This file
├── USER.md            # Who you work for
├── HEARTBEAT.md       # Heartbeat task checklist
├── daily/             # Daily observation logs (YYYY-MM-DD.md)
├── reports/           # Task prompt files
├── state/             # Logs and runtime state
│   ├── service.log    # Session output
│   ├── service.pid    # Background process PID
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
- `skills/daemon-management.md` — Service start/stop/restart/status, launchd/systemd
- `skills/config-reference.md` — Full leo.yaml field reference
- `skills/workspace-maintenance.md` — Daily logs, MCP config, workspace hygiene
- `skills/agent-management.md` — Spawn and manage ephemeral coding agents

## Common Operations

### Check status
```bash
leo service status              # Service running?
leo service status --daemon     # OS service status
leo status                      # Overall status
leo task list                   # Configured tasks
leo validate                    # Config and prereq health check
```

### Read recent logs
```bash
tail -50 {{.Workspace}}/state/service.log
tail -50 {{.Workspace}}/state/<task>.log
```

### Manage tasks
```bash
leo task add                 # Interactive task creation
leo task enable <name>       # Enable a disabled task
leo task disable <name>      # Disable without removing
leo task remove <name>       # Remove from config
```

### Run a task manually
```bash
leo run <task>               # Execute now
leo run <task> --dry-run     # Show assembled prompt only
```
