# leo setup

Interactive setup wizard for creating a new agent.

## Usage

```bash
leo setup
```

## Description

The setup wizard guides you through configuring a new agent from scratch. It creates the workspace directory, writes the config file, generates the agent personality file, and optionally installs cron entries.

## Wizard Steps

1. **Agent name** — choose a name for your agent (e.g., `leo`)
2. **Workspace directory** — where config, prompts, and logs will live (default: `~/leo`)
3. **Personality template** — select from [chief-of-staff, dev-assistant, or skeleton](../guides/agent-templates.md)
4. **User profile** — your name, role, preferences, and timezone
5. **Telegram connection** — paste your bot token, then send a message to your bot so Leo can detect your chat ID
6. **MCP servers** — optionally configure integrations (calendar, email, etc.)
7. **Scheduled tasks** — add recurring tasks with cron expressions
8. **Cron installation** — write task schedules to your system crontab
9. **Test message** — send a test message to verify Telegram is working

## What It Creates

- `<workspace>/leo.yaml` — configuration file
- `<workspace>/USER.md` — user profile
- `<workspace>/HEARTBEAT.md` — heartbeat checklist template
- `<workspace>/daily/` — directory for daily logs
- `<workspace>/reports/` — directory for task prompts
- `<workspace>/state/` — directory for runtime logs
- `~/.claude/agents/<name>.md` — Claude agent personality file

## Re-Running Setup

Running `leo setup` again on an existing workspace will overwrite the configuration. Use `leo onboard` for a guided flow that detects existing installations and offers reconfiguration options.

## See Also

- [`leo onboard`](onboard.md) — guided first-time setup with prerequisite checks
- [Agent Templates](../guides/agent-templates.md) — details on each personality template
- [Telegram Setup](../guides/telegram-setup.md) — detailed bot creation walkthrough
