# Directory Structure

Leo uses a home directory at `~/.leo/` that holds config, state, and the default workspace.

## Directory Layout

```
~/.leo/
├── leo.yaml                    # Leo config
├── workspace/                  # Default workspace
│   ├── CLAUDE.md               # Agent instructions (generated)
│   ├── USER.md                 # Your profile (created during setup)
│   ├── prompts/                # Task prompt files
│   ├── skills/                 # Agent skill files (generated)
│   │   ├── managing-tasks.md
│   │   ├── debugging-logs.md
│   │   ├── daemon-management.md
│   │   ├── config-reference.md
│   │   ├── workspace-maintenance.md
│   │   └── agent-management.md
│   └── config/
│       └── mcp-servers.json    # MCP server configuration
├── agents/                     # Default agent workspace (for templates)
└── state/                      # Runtime state and logs
    ├── sessions.json           # Process session ID mappings
    ├── agents.json             # Ephemeral agent records (for restart recovery)
    ├── task-history.json       # Task execution history
    ├── topics.json             # Cached Telegram forum topics
    ├── service.log             # Service log output
    ├── service.pid             # Background process PID file
    └── leo.sock                # Daemon Unix socket for CLI IPC
```

## Key Paths

### `~/.leo/leo.yaml`

The main configuration file. See [Configuration](index.md) for details.

### `~/.leo/workspace/`

The default workspace directory. Processes and tasks use this workspace unless they specify their own `workspace` field. The workspace is passed to Claude via `--add-dir` so the assistant can read and write files here.

### `~/.leo/workspace/CLAUDE.md`

Generated agent instructions that give Claude context about Leo's capabilities. Rendered from a template during setup and refreshed by `leo update`.

### `~/.leo/workspace/USER.md`

A user profile filled in during setup. Included in the assistant's context so it knows who you are, your role, preferences, and timezone.

### `~/.leo/workspace/skills/`

Auto-generated skill files that teach the assistant how to manage tasks, read logs, control the daemon, and dispatch agents. Refreshed by `leo update`.

### `~/.leo/workspace/config/mcp-servers.json`

MCP server configuration passed to Claude via `--mcp-config`. Configure integrations like Google Calendar, Gmail, or custom tools here.

### `~/.leo/agents/`

Default base directory for ephemeral agent workspaces. When you dispatch `/agent coding owner/repo`, the repo is cloned into `~/.leo/agents/repo/`. Templates can override this with their own `workspace` field.

For worktree spawns (`leo agent spawn ... --worktree <branch>`), the canonical clone stays at `~/.leo/agents/<repo>/` and each branch gets its own checkout under:

```
~/.leo/agents/.worktrees/<repo-short>/<branch-slug>/
```

Multiple agents can run in parallel against the same repo without stepping on each other's working tree. The agentstore record stores both paths — `workspace` (the worktree) and `canonical_path` (the shared clone).

### `~/.leo/state/`

Runtime files managed by Leo:

- **`sessions.json`** -- process name to Claude session UUID mappings (for `--resume`)
- **`agents.json`** -- ephemeral agent records, used to restore agents after daemon restart
- **`task-history.json`** -- execution history for scheduled tasks
- **`topics.json`** -- cached Telegram forum topic IDs
- **`service.log`** -- output from the daemon (all supervised processes)
- **`service.pid`** -- PID file for the background service
- **`leo.sock`** -- Unix socket for CLI-to-daemon IPC

## Custom Workspaces

Processes, tasks, and templates can each specify their own `workspace`:

```yaml
processes:
  coding:
    workspace: ~/projects/my-app

tasks:
  repo-check:
    workspace: ~/projects/my-app

templates:
  coding:
    workspace: ~/agents
```

When no `workspace` is specified, processes and tasks use `~/.leo/workspace/`, and templates use `~/.leo/agents/`.
