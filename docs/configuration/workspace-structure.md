# Directory Structure

Leo uses a home directory at `~/.leo/` that holds config, state, and the default workspace.

## Directory Layout

```
~/.leo/
├── leo.yaml                    # Leo config
├── workspace/                  # Default workspace
│   ├── USER.md                 # Your profile (created during setup)
│   ├── reports/                # Task prompt files
│   ├── config/
│   │   └── mcp-servers.json    # MCP server configuration
│   └── scripts/                # Helper scripts (optional)
└── state/                      # Runtime state and logs
    ├── service.log             # Service session log
    ├── service.pid             # Background process PID file
    ├── topics.json             # Cached Telegram forum topics
    └── <task>.log              # Per-task execution logs
```

## Key Paths

### `~/.leo/leo.yaml`

The main configuration file. See [Configuration](index.md) for details.

### `~/.leo/workspace/`

The default workspace directory. Processes and tasks use this workspace unless they specify their own `workspace` field. The workspace is passed to Claude via `--add-dir` so the assistant can read and write files here.

### `~/.leo/workspace/USER.md`

A user profile filled in during setup. This is included in the assistant's context so it knows who you are, your role, preferences, and timezone.

### `~/.leo/workspace/reports/`

Store your task prompt files here. Each task in `leo.yaml` references a `prompt_file` relative to its workspace, e.g., `reports/daily-news-briefing.md`.

### `~/.leo/workspace/config/mcp-servers.json`

MCP server configuration passed to Claude via `--mcp-config`. Configure integrations like Google Calendar, Gmail, or custom tools here.

### `~/.leo/state/`

Runtime files managed by Leo:

- **`service.log`** -- output from the service (all supervised processes)
- **`service.pid`** -- PID file for the background service (`leo service start`)
- **`topics.json`** -- cached Telegram forum topic IDs
- **`<task>.log`** -- stdout/stderr from each task execution, useful for debugging

### `~/.leo/workspace/scripts/`

An optional directory for helper scripts your assistant or tasks might use.

## Custom Workspaces

Processes and tasks can each specify their own `workspace` to use a different directory:

```yaml
processes:
  coding:
    workspace: ~/projects/my-app
    # ...

tasks:
  repo-check:
    workspace: ~/projects/my-app
    # ...
```

When no `workspace` is specified, Leo uses `~/.leo/workspace/`.
