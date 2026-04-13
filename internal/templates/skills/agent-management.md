# Agent Management

Leo can spawn and manage ephemeral coding agents via its HTTP API. These agents are Claude Code sessions running in tmux with `--remote-control`, accessible from claude.ai/code.

## API Endpoints

All endpoints are on `http://127.0.0.1:${LEO_WEB_PORT}` (default port 8370).

### Spawn an Agent

```
POST /api/agent/spawn
Content-Type: application/json

{
  "template": "coding",
  "repo": "owner/repo"       // clones from GitHub if not local
  // OR
  "repo": "project-name"     // uses template workspace, no cloning
}
```

Response: `{"ok": true, "data": {"name": "leo-coding-repo", "workspace": "/path/to/workspace"}}`

### List Running Agents

```
GET /api/agent/list
```

Response: `{"ok": true, "data": {"leo-coding-leo": {"name": "...", "status": "running", ...}}}`

### Stop an Agent

```
POST /api/agent/stop
Content-Type: application/json

{"name": "leo-coding-leo"}
```

`name` accepts shorthand — a repo short name (`"leo"`), a full `owner/repo`, or a suffix of the full agent name will all resolve as long as the match is unambiguous among running agents. The server returns an error listing candidates when the query matches multiple live agents.

### List Available Templates

```
GET /api/template/list
```

## Telegram Commands

- `/agent` — interactive template + repo selection
- `/agent coding owner/repo` — shorthand spawn
- `/agents` — list running agents with stop buttons
- `/stop` — interrupt the current Claude operation

## Notes

- Agents use `--remote-control` so they appear in claude.ai/code
- Agent names follow the pattern: `leo-{template}-{owner}-{repo-short}` for `owner/repo` spawns, or `leo-{template}-{name}` for bare-name spawns
- Stop/logs/session APIs accept shorthand: any unambiguous repo short, full `owner/repo`, or agent-name suffix resolves to the canonical record
- Agents are ephemeral — they're not persisted to leo.yaml but survive daemon restarts via agents.json
- Templates are defined in the `templates:` section of leo.yaml
