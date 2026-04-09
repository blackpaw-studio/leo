# Quick Start

Get a personal AI assistant running on Telegram in under 5 minutes.

## 1. Install Leo

=== "Homebrew"

    ```bash
    brew install blackpaw-studio/tap/leo
    ```

=== "Go"

    ```bash
    go install github.com/blackpaw-studio/leo@latest
    ```

See [Installation](installation.md) for all options.

## 2. Run the Setup Wizard

```bash
leo setup
```

The interactive wizard walks you through:

1. **Creating a user profile** -- tells the assistant who you are
2. **Connecting Telegram** -- paste your bot token, then send a message to auto-detect your chat ID
3. **Configuring MCP servers** -- optional integrations (calendar, email, etc.)
4. **Adding scheduled tasks** -- recurring jobs like daily briefings
5. **Installing cron entries** -- writes tasks to your system crontab
6. **Sending a test message** -- verifies everything works end-to-end

!!! tip "Don't have a Telegram bot yet?"
    The wizard will prompt you to create one. See the [Telegram Setup](../guides/telegram-setup.md) guide for a detailed walkthrough of the BotFather process.

## 3. Start the Service

Launch the Telegram session:

```bash
leo service start
```

Your assistant is now listening. Send a message to your bot on Telegram and it will respond.

!!! info "Background operation"
    `leo service start` runs all enabled processes in the background with automatic restart. For daemon mode (persists across reboots), see [Background Mode](../guides/background-mode.md).

## 4. Test a Scheduled Task

If you added a task during setup, run it manually:

```bash
leo run <task-name>
```

If the agent has something to report, you'll receive a Telegram message. If there's nothing noteworthy, it exits silently.

## 5. Verify Cron

Check that your scheduled tasks are installed:

```bash
leo cron list
```

This shows the cron entries Leo manages in your system crontab.

---

## What's Next

- [Telegram Setup](../guides/telegram-setup.md) -- detailed guide to creating and configuring your bot
- [Scheduling](../guides/scheduling.md) -- deep dive into cron expressions, timezones, and silent mode
- [Background Mode](../guides/background-mode.md) -- keep your service alive across reboots
- [Writing Tasks](../guides/writing-tasks.md) -- create custom scheduled tasks
