# Workspace Maintenance

## Memory

Leo does not include a built-in memory system. To add persistent memory, configure an MCP memory server in `config/mcp-servers.json`. This file is passed to Claude via `--mcp-config` in both chat and task modes.

Popular options include [Basic Memory](https://github.com/basicmachines-co/basic-memory) (Markdown + SQLite + semantic search) and [Mem0](https://mem0.ai/) (cloud or self-hosted). Any MCP server that provides memory tools will work.

## Daily Logs (daily/)

Write observations, decisions, and notes to `daily/YYYY-MM-DD.md`.

### Convention
- **Append**, don't overwrite — multiple sessions may write to the same day
- Keep entries concise and timestamped
- Note decisions made, issues found, follow-ups needed
- These are your working notes, not polished reports

### Cleanup
Old daily logs can be archived or removed. They are reference material, not permanent records.

## Reports (reports/)

Task prompt files live here. Each file contains the instructions for a scheduled task.

### Creating a new prompt
1. Write the prompt file in `reports/<name>.md`
2. Add the task to config: `leo task add`
3. Install cron: `leo cron install`

## MCP Config (config/mcp-servers.json)

Add MCP server configurations here. This file is passed to Claude via `--mcp-config` in both chat and task modes.

Format:
```json
{
  "mcpServers": {
    "server-name": {
      "command": "npx",
      "args": ["-y", "@some/mcp-server"]
    }
  }
}
```

## Scripts (scripts/)

Helper scripts for automation. These are available to the agent via Bash tool since the workspace is added with `--add-dir`.

## General Hygiene

- **State logs** (`state/*.log`) grow over time. They can be truncated or rotated as needed.
- **PID files** (`state/chat.pid`) are managed by Leo — don't edit manually.
- **Config** (`leo.yaml`) is the source of truth. Edit it directly or use `leo task` commands.
- **Agent file** (`~/.claude/agents/<name>.md`) defines your persona. Edit it to change behavior.
