# leo setup

Interactive setup wizard.

## Usage

```bash
leo setup
```

## Description

The setup wizard guides you through configuring a new workspace from scratch. It creates the workspace directory, writes the config file, and optionally installs cron entries.

## Wizard Steps

1. **Workspace directory** — where config, prompts, and logs will live (default: `~/leo`)
2. **User profile** — your name, role, preferences, and timezone
3. **Telegram connection** — paste your bot token, then send a message to your bot so Leo can detect your chat ID
4. **MCP servers** — optionally configure integrations (calendar, email, etc.)
5. **Scheduled tasks** — add recurring tasks with cron expressions
6. **Cron installation** — write task schedules to your system crontab
7. **Test message** — send a test message to verify Telegram is working

## What It Creates

- `<workspace>/leo.yaml` — configuration file
- `<workspace>/USER.md` — user profile
- `<workspace>/HEARTBEAT.md` — heartbeat checklist template
- `<workspace>/daily/` — directory for daily logs
- `<workspace>/reports/` — directory for task prompts
- `<workspace>/state/` — directory for runtime logs

## Re-Running Setup

Running `leo setup` again on an existing workspace will overwrite the configuration. Use `leo onboard` for a guided flow that detects existing installations and offers reconfiguration options.

## See Also

- [`leo onboard`](onboard.md) — guided first-time setup with prerequisite checks
- [Telegram Setup](../guides/telegram-setup.md) — detailed bot creation walkthrough
