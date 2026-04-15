# Quick Start

Get a personal AI assistant running in under 5 minutes.

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

1. **Creating a user profile** — tells the assistant who you are
2. **Picking a workspace** — the root directory for your agent's files
3. **Scaffolding `CLAUDE.md` and skills** — baseline context for the agent
4. **Configuring MCP servers** (optional) — integrations (calendar, email, etc.)
5. **Adding scheduled tasks** (optional) — recurring jobs like daily briefings
6. **Optionally installing as a daemon** — for persistence across reboots

The wizard does **not** install or configure any messaging channel. Leo is channel-agnostic.

## 3. (Optional) Install a Channel Plugin

If you want mobile access or another way to chat with your assistant, install a Claude Code channel plugin separately. For example, to enable Telegram:

```bash
claude plugin install telegram@claude-plugins-official
```

Follow the plugin's own setup flow to connect your bot. Then reference the plugin in `leo.yaml`:

```yaml
processes:
  assistant:
    channels: [plugin:telegram@claude-plugins-official]
    enabled: true
```

Leo passes the list to the spawned Claude process via the `LEO_CHANNELS` environment variable; the plugin owns its own auth and routing.

## 4. Start the Service

```bash
leo service start
```

Your assistant is now listening. If you configured a channel plugin, send a message to it and the agent will respond.

!!! info "Background operation"
    `leo service start` runs all enabled processes in the background with automatic restart. For daemon mode (persists across reboots), see [Background Mode](../guides/background-mode.md).

## 5. Test a Scheduled Task

If you added a task during setup, run it manually:

```bash
leo run <task-name>
```

If the agent has something to report, you'll receive a message on your configured channel. If there's nothing noteworthy, it outputs `NO_REPLY`.

## 6. Verify Your Schedule

Check that your scheduled tasks are loaded and see when each will fire next:

```bash
leo task list
```

When the daemon is running, the `NEXT RUN` column shows the upcoming fire time for each task. Leo runs its own in-process scheduler — there is no system crontab to install.

---

## What's Next

- [Scheduling](../guides/scheduling.md) — deep dive into cron expressions, timezones, and silent mode
- [Background Mode](../guides/background-mode.md) — keep your service alive across reboots
- [Writing Tasks](../guides/writing-tasks.md) — create custom scheduled tasks
- [Configuration](../configuration/config-reference.md) — full leo.yaml reference
