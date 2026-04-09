# leo setup

Interactive setup wizard.

## Usage

```bash
leo setup
```

## Description

The setup wizard guides you through configuring Leo from scratch. It creates the Leo home directory, writes the config file, and optionally installs cron entries.

## Wizard Steps

1. **User profile** -- your name, role, preferences, and timezone
2. **Telegram connection** -- paste your bot token, then send a message to your bot so Leo can detect your chat ID
3. **MCP servers** -- optionally configure integrations (calendar, email, etc.)
4. **Scheduled tasks** -- add recurring tasks with cron expressions
5. **Cron installation** -- write task schedules to your system crontab
6. **Test message** -- send a test message to verify Telegram is working

## What It Creates

- `~/.leo/leo.yaml` -- configuration file
- `~/.leo/workspace/USER.md` -- user profile
- `~/.leo/workspace/reports/` -- directory for task prompts
- `~/.leo/state/` -- directory for runtime logs

## Re-Running Setup

Running `leo setup` again will overwrite the configuration. Use `leo onboard` for a guided flow that detects existing installations and offers reconfiguration options.

## See Also

- [`leo onboard`](onboard.md) -- guided first-time setup with prerequisite checks
- [Telegram Setup](../guides/telegram-setup.md) -- detailed bot creation walkthrough
