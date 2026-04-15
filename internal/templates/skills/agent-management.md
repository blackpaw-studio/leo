# Agent Management

Leo can spawn and manage ephemeral coding agents two ways: the HTTP API (used by channel plugins and the web UI) and the `leo agent` CLI (Bash tool, SSH). Both share one in-memory manager so state is always consistent. Agents run in tmux with `--remote-control` and appear in claude.ai/code.

When you need multiple agents working on the same repo in parallel, use `leo agent spawn --worktree <branch>` — it creates an isolated git worktree per branch so nothing fights over `.git/HEAD`. The HTTP API only supports the shared-workspace flow today; reach for the CLI when you need branch isolation.

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

## CLI (Bash tool)

The `leo agent` CLI covers everything the HTTP API does plus worktree spawning and pruning:

```bash
leo agent spawn coding --repo owner/repo                         # shared-workspace spawn (same as HTTP)
leo agent spawn coding --repo owner/repo --worktree feat/cache   # dedicated git worktree
leo agent spawn coding --repo owner/repo --worktree new/idea --base main
leo agent list                                                   # includes a BRANCH column
leo agent stop <name>                                            # kill tmux session
leo agent stop <name> --prune --delete-branch                    # stop + remove worktree + delete branch
leo agent prune <name>                                           # clean up a stopped worktree agent
```

Rules to remember:

- `--worktree` requires `owner/repo` (slashless repos have no canonical clone to branch from).
- Worktree checkouts live at `<baseWorkspace>/.worktrees/<repo-short>/<branch-slug>/`; the canonical clone stays at `<baseWorkspace>/<repo-short>/`.
- Stopping a worktree agent **keeps** the record and the checkout so you can reattach or inspect the branch — run `leo agent prune` (or `stop --prune`) to tear it down.
- `prune` takes the canonical agent name only. Stopped agents aren't in the shorthand resolver, so look them up via `leo agent list` first.
- Typed error codes surfaced by prune: `worktree_dirty`, `branch_not_merged`, `branch_checked_out`, `agent_still_running`, `not_worktree_agent`. Pass `--force` to override the first two.

## Channel Plugin Integration

If you have a channel plugin installed (e.g. Telegram, Slack) that exposes agent-management commands, those are provided by the plugin itself — not by Leo. Consult the plugin's own docs for the command surface. Leo only exposes the HTTP API above; the plugin translates channel commands into HTTP calls against `${LEO_WEB_PORT}`.

## Notes

- Agents use `--remote-control` so they appear in claude.ai/code
- Agent names follow the pattern:
  - `leo-{template}-{owner}-{repo-short}` for `owner/repo` spawns
  - `leo-{template}-{name}` for bare-name spawns
  - `leo-{template}-{owner}-{repo-short}-{branch-slug}` for `--worktree` spawns
- Stop/logs APIs accept shorthand: any unambiguous repo short, full `owner/repo`, or agent-name suffix resolves to the canonical record
- Agents are ephemeral — they're not persisted to leo.yaml but survive daemon restarts via `agents.json`. Worktree agents record both `workspace` (the checkout) and `canonical_path` (the shared clone); restore stat-checks the workspace and drops records whose checkout has been removed externally.
- Templates are defined in the `templates:` section of leo.yaml
