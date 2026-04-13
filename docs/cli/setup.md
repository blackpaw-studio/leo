# leo setup

Interactive setup wizard.

## Usage

```bash
leo setup
```

## Description

The setup wizard guides you through configuring Leo from scratch. It creates the Leo home directory and writes the config file.

## Wizard Steps

1. **User profile** -- your name, role, preferences, and timezone
2. **Telegram connection** -- paste your bot token, then send a message to your bot so Leo can detect your chat ID
3. **MCP servers** -- optionally configure integrations (calendar, email, etc.)
4. **Scheduled tasks** -- add recurring tasks with cron expressions
5. **Test message** -- send a test message to verify Telegram is working

## What It Creates

- `~/.leo/leo.yaml` -- configuration file
- `~/.leo/workspace/USER.md` -- user profile
- `~/.leo/workspace/reports/` -- directory for task prompts
- `~/.leo/state/` -- directory for runtime logs

## Re-Running Setup

Running `leo setup` again will overwrite the configuration. Back up `~/.leo/leo.yaml` first if you have customizations worth keeping.

## See Also

- [Telegram Setup](../guides/telegram-setup.md) -- detailed bot creation walkthrough
- [`leo validate`](index.md#leo-validate) -- sanity-check your config after edits
