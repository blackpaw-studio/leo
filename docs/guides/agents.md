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

### From the CLI

```bash
leo agent spawn coding --repo blackpaw-studio/leo --name demo
leo agent list
leo agent attach demo      # full tmux attach to the Claude TUI
leo agent logs demo -n 100
leo agent stop demo
```

Run these locally on the server, or from a laptop against a remote host by adding a `client.hosts` section to `leo.yaml`. See the [Remote CLI guide](remote-cli.md) and the [`leo agent` reference](../cli/agent.md).

### From the JSON API

The daemon exposes both a Unix-socket API (used by the CLI) and an HTTP API on the web port (used by the Telegram plugin and web UI):

```
POST /agents/spawn        {"template": "coding", "repo": "owner/repo"}   (daemon socket)
GET  /agents/list                                                         (daemon socket)
POST /agents/{name}/stop                                                  (daemon socket)
GET  /agents/{name}/logs?lines=N                                          (daemon socket)
GET  /agents/{name}/session                                               (daemon socket)

POST /api/agent/spawn     {"template": "coding", "repo": "owner/repo"}   (web HTTP)
POST /api/agent/stop      {"name": "leo-coding-owner-repo"}              (web HTTP)
GET  /api/agent/list                                                      (web HTTP)
```

Both transports share the same `internal/agent` manager, so state stays consistent across CLI, web UI, and Telegram.

### Shorthand Names

CLI, daemon API, and web handlers all resolve a shorthand query against live agents before performing an action. The resolver tries these tiers in order and picks the first unambiguous match:

1. Exact full name (case-insensitive)
2. Exact stored repo (`owner/name`)
3. Repo short — the segment after the slash, or the full value for slashless repos
4. Suffix match on the full name (`-<query>`)

So if only one live agent targets `blackpaw-studio/leo`, any of these work: `leo`, `blackpaw-studio/leo`, `leo-coding-blackpaw-studio-leo`. Ambiguous queries are rejected with the list of matching names. Stopped agents are never considered — the short name is free again the moment an agent exits.

The daemon also exposes `GET /agents/resolve?q=<query>` over the Unix socket for read-only lookups (returns the canonical name, tmux session, and stored repo).

## Managing Agents

### Listing

- **Telegram:** `/agents` — shows running agents with stop buttons
- **Web UI:** agents panel on the dashboard
- **CLI:** `leo agent list` (`--json` for scripting)
- **API:** `GET /api/agent/list`

### Attaching

- **CLI:** `leo agent attach <name>` — full tmux attach, same TUI as running Claude locally. Works remotely via `ssh -t <host> tmux attach -t leo-<name>` when `client.hosts` is configured.

### Stopping

- **Telegram:** tap the stop button next to an agent in `/agents`
- **Web UI:** stop button in the agent panel
- **CLI:** `leo agent stop <name>`
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
