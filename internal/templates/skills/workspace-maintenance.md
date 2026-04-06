# Workspace Maintenance

## Memory (MEMORY.md)

`MEMORY.md` is symlinked to `~/.claude/agent-memory/<name>/MEMORY.md` and persists across sessions.

### Guidelines
- Keep it under **200 lines** — curate actively
- Remove stale entries (completed projects, resolved issues)
- Update changing facts (current priorities, recent decisions)
- Organize by topic, not chronologically
- Track: ongoing projects, deadlines, user preferences, recurring issues

### What NOT to store
- Information derivable from code or git history
- Ephemeral task details or temporary state
- Debugging solutions (the fix is in the code)

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
