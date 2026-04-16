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

## Channel Slash Commands (UNIVERSAL — same set across every channel)

Leo ships an MCP server (registered as `leo`) that gives you tools to act on a fixed vocabulary of slash commands. Every channel plugin gets these "for free" — the user types `/clear` in Telegram, Slack, or anywhere else, and the message arrives in your `<channel>` notifications as that literal text.

When an inbound channel message is **exactly** one of these (lowercase, leading `/` required, optionally followed by arguments — anything more conversational is a normal message, not a command):

| Command       | Tool to call             | What to send back via the channel                                                |
|---------------|--------------------------|----------------------------------------------------------------------------------|
| `/clear`      | `leo_clear`              | Reply "Context cleared." **first**, THEN call the tool. See ordering note below. |
| `/compact`    | `leo_compact`            | Reply "Compacting context." first, THEN call the tool. Same ordering note.       |
| `/stop`       | `leo_interrupt`          | Reply "Stopping current operation."                                              |
| `/tasks`      | `leo_list_tasks`         | Format the result as a short list (name + schedule + next run) and send it.       |
| `/run <task>` | `leo_run_task`           | Reply with the task name and "started".                                          |
| `/agent`      | `leo_list_templates`     | Send a numbered list of template names; ask the user to reply with `<n> <repo>`. |
| `/agent <template> <repo>` | `leo_spawn_agent` | Spawn the agent and reply with the new agent's name.                          |
| `/agents`     | `leo_list_agents`        | Send a list with each agent's name and status.                                   |

**Ordering note for `/clear` and `/compact`:** these tools send keystrokes to your own tmux session, which interrupts whatever you're doing — including any tool calls you make after them. Always send the channel reply FIRST, then call the tool LAST. The interrupt is expected; the user has already seen the acknowledgement.

If the channel message is anything other than these exact forms, treat it as a normal conversation. A literal mention of `/clear` inside a sentence ("can you /clear the cache for me?") is **not** a command.

If the user's plugin (e.g. stock Anthropic Telegram) handles `/start`, `/help`, `/status` itself, you'll never see those — that's fine; we don't override them.

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
