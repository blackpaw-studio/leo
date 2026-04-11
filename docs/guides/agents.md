# Agent Templates

Leo can spawn ephemeral coding agents on demand from reusable templates. Each agent runs in its own tmux session with an isolated workspace, and can be accessed via claude.ai/code or the Claude desktop/web app.

## Defining Templates

Add templates to your `leo.yaml`:

```yaml
templates:
  coding:
    model: sonnet
    remote_control: true
    permission_mode: bypassPermissions
    workspace: ~/agents
```

Templates support the same fields as processes (model, channels, permission_mode, allowed_tools, env, etc.). See the [config reference](../configuration/config-reference.md#templates) for all fields.

## Dispatching Agents

### From Telegram

Send `/agent <template> <owner/repo>` to your bot:

```
/agent coding blackpaw-studio/leo
```

This will:

1. Clone `blackpaw-studio/leo` into `~/agents/leo` (using `gh`)
2. Start a Claude session in a new tmux session
3. Name the session `leo-coding-blackpaw-studio-leo`
4. Reply with the agent name for connecting via Claude web or app

If the repo is already cloned, Leo reuses the existing checkout.

You can also send just `/agent` to get an interactive template picker with inline buttons.

### From the Web UI

The web dashboard has an agent panel where you can spawn and stop agents. Navigate to your Leo dashboard and use the agent section.

### From the JSON API

The daemon exposes a JSON API used by the Telegram plugin:

```
POST /api/agent/spawn   {"template": "coding", "repo": "owner/repo"}
POST /api/agent/stop    {"name": "leo-coding-owner-repo"}
GET  /api/agent/list
```

## Managing Agents

### Listing

- **Telegram:** `/agents` — shows running agents with stop buttons
- **Web UI:** agents panel on the dashboard
- **API:** `GET /api/agent/list`

### Stopping

- **Telegram:** tap the stop button next to an agent in `/agents`
- **Web UI:** stop button in the agent panel
- **API:** `POST /api/agent/stop {"name": "..."}`

Stopping an agent kills its tmux session and removes it from the agent store.

## Session Naming

Agents are named based on the template and repo:

| Input | Agent Name |
|-------|------------|
| `/agent coding owner/repo` | `leo-coding-owner-repo` |
| `/agent coding my-project` | `leo-coding-my-project` |

This name is used as the `--name` flag for Claude, so it appears exactly as shown in claude.ai/code and the Claude app.

If a name collides with an existing agent, Leo appends `-2`, `-3`, etc.

## Persistence

Agent records are stored in `~/.leo/state/agents.json`. When the daemon restarts, it checks if each agent's tmux session is still alive and re-registers surviving sessions with the supervisor. Dead sessions are cleaned up automatically.

## Supervisor Behavior

Ephemeral agents use the same supervisor as regular processes:

- Auto-restart on exit with exponential backoff
- Quick-exit detection (< 15s) clears stale sessions
- Resume prompt auto-dismissal (the "Resume from summary" prompt is handled automatically)
- Telegram plugin lock file monitoring (for agents with Telegram channels)
