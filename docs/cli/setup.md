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
2. **Workspace** -- pick the root directory for your agent's files
3. **Scaffold `CLAUDE.md` and skills** -- baseline context for the agent
4. **MCP servers** -- optionally configure integrations (calendar, email, etc.)
5. **Scheduled tasks** -- add recurring tasks with cron expressions
6. **Daemon install** -- optionally install as a launchd/systemd service

The wizard does **not** install any messaging channel. Leo is channel-agnostic — install a Claude Code channel plugin separately (e.g. `claude plugin install telegram@claude-plugins-official`) and reference its ID in your process or task `channels:` list.

## What It Creates

- `~/.leo/leo.yaml` -- configuration file
- `~/.leo/workspace/USER.md` -- user profile
- `~/.leo/workspace/reports/` -- directory for task prompts
- `~/.leo/state/` -- directory for runtime logs

## Re-Running Setup

Running `leo setup` again will overwrite the configuration. Back up `~/.leo/leo.yaml` first if you have customizations worth keeping.

## See Also

- [Configuration → Channels](../configuration/config-reference.md#channels) -- wiring up a channel plugin
- [`leo validate`](validate.md) -- sanity-check your config after edits
