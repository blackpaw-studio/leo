# leo migrate

Migrate from an existing OpenClaw installation to Leo.

## Usage

```bash
leo migrate
```

## Description

If you have an existing OpenClaw installation, `leo migrate` converts your setup to Leo's format. It migrates:

- Workspace directory and files
- Agent personality files
- Cron job entries
- Telegram bot configuration

The migration is interactive and will prompt you before making changes.

## When to Use

Use `leo migrate` if you previously used OpenClaw and want to switch to Leo. If you're starting fresh, use [`leo setup`](setup.md) instead.

!!! tip "Auto-detection"
    [`leo onboard`](onboard.md) automatically detects OpenClaw installations and offers to run the migration for you.

## See Also

- [`leo onboard`](onboard.md) — guided setup that detects existing installations
- [`leo setup`](setup.md) — fresh setup wizard
