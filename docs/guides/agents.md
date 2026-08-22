# Agent Templates

Leo can spawn ephemeral coding agents on demand from reusable templates. Each agent runs in its own tmux session with an isolated workspace, and can be accessed via claude.ai or the Claude desktop/mobile app.

## Defining Templates

Add templates to your `leo.yaml`:

```yaml
templates:
  coding:
    model: sonnet
    workspace: ~/agents
    harness_options:
      remote_control: true
      permission_mode: auto
```

Templates support model, channels, harness, harness_options, env, and more. They also back **persistent tasks** — a `runtime: persistent` task can target a template's agent instead of spawning `claude -p` per firing; see [Persistent Tasks](../configuration/persistent-tasks.md). See the [config reference](../configuration/config-reference.md#templates) and [Harnesses](../configuration/harnesses.md) for all fields.

## Dispatching Agents

### From a channel plugin

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
leo agent spawn coding                                     # run the template as-is; agent named "coding"
leo agent spawn coding --repo blackpaw-studio/leo --name demo
leo agent list
leo agent attach demo      # full tmux attach to the Claude TUI
leo agent logs demo -n 100
leo agent stop demo
```

`--repo` is optional. Omit it to run the template directly in its own workspace, with the agent named after the template (`--name` still overrides, and a collision appends `-2`, `-3`, ...).

Run these locally on the server, or from a laptop against a remote host by adding a `client.hosts` section to `leo.yaml`. See the [Remote CLI guide](remote-cli.md) and the [`leo agent` reference](../cli/agent.md).

#### Worktree Spawns

Pass `--worktree <branch>` to isolate the agent in its own git worktree off the canonical clone:

```bash
leo agent spawn coding --repo blackpaw-studio/leo --worktree feat/cache
leo agent spawn coding --repo blackpaw-studio/leo --worktree fix/bug --base main
leo agent stop feat-cache
leo agent delete feat-cache --delete-branch   # stop, then clean up
```

Worktree agents run in parallel on the same repo without fighting over `.git/HEAD` — every branch gets its own checkout under `<baseWorkspace>/.worktrees/<repo-short>/<branch-slug>/`. The agent name includes the branch slug, and `leo agent list` shows a `BRANCH` column for worktree agents. See the [`leo agent` reference](../cli/agent.md#worktree-spawns) for the full flag set.

#### Branching From an Existing Agent

`--worktree` requires `--repo owner/repo`. If you'd rather branch off an *agent* you already have running — regardless of how it was spawned — use the shorthand form instead:

```bash
leo agent worktree chronicle a11y                                          # chronicle-a11y, branched off chronicle's workspace
leo agent worktree chronicle hotfix --base v1.2.0 --prompt "fix the crash"
```

This works for any agent whose workspace is a git repo, no `owner/repo` needed — the source agent's template and env are inherited by default, and its workspace (or canonical repo, if the source is itself a worktree agent — pass `--base <its-branch>` to fork from that branch) becomes the new agent's git canonical. Pass `--template <name>` to run the same source repo under a different template — the git canonical stays tied to the source agent, but the override template must already exist in config and none of the source agent's env carries over (only the override template's own env plus `--env`). Remoteless repos skip the fetch step and branch off `HEAD` instead of origin's default branch. Naming and cleanup match ordinary worktree agents: `<agent>-<branch-slug>` — stop it, then `leo agent delete <name> --delete-branch` tears it down. See the [`leo agent worktree` reference](../cli/agent.md#leo-agent-worktree-agent-branch) for the full flag set.

### From the JSON API

The daemon exposes both a Unix-socket API (used by the CLI) and an HTTP API on the web port (used by the channel plugin and web UI):

```
POST /agents/spawn        {"template": "coding", "repo": "owner/repo", "branch": "feat/x"}  (daemon socket; "repo" is optional)
GET  /agents/list                                                                            (daemon socket)
POST /agents/{name}/stop                                                                     (daemon socket)
POST /agents/{name}/start                                                                    (daemon socket)
DELETE /agents/{name} {"force": false, "delete_branch": false}                               (daemon socket)
GET  /agents/{name}/logs?lines=N                                                             (daemon socket)
GET  /agents/{name}/session                                                                  (daemon socket)

POST /api/agent/spawn     {"template": "coding", "repo": "owner/repo"}   (web HTTP)
POST /api/agent/stop      {"name": "leo-coding-owner-repo"}              (web HTTP)
GET  /api/agent/list                                                      (web HTTP)
```

On `/agents/spawn`, `branch` is optional — when present the daemon creates a worktree and the response includes `branch` and `canonical_path`. `DELETE /agents/{name}` removes a stopped agent's record — and, for a worktree agent, its checkout too; it returns typed error codes (`worktree_dirty`, `branch_not_merged`, `agent_still_running`) so clients can dispatch on `errors.Is`.

Both transports share the same `internal/agent` manager, so state stays consistent across CLI, web UI, and any channel plugin that invokes the HTTP API.

### Listings do not carry env values

`GET /api/agent/list` and `GET /api/template/list` are what the `leo_list_agents` and `leo_list_templates` MCP tools serve to any agent that calls them, and `GET /task/list` is reachable by anything that can open the daemon socket. None of the three include env values: agent records carry no env at all, while template and task records carry `env_keys` (key names only). `leo template show` and `leo run --dry-run` mask credential-looking values the same way — read `leo.yaml` directly when you need a real value.

Agent env also travels to tmux as `new-session -e` argv rather than shell exports, so it is no longer part of the pane's start command — `list-panes -F '#{pane_start_command}'` used to return every agent's credentials for the life of the session. The values are still in the short-lived tmux *client's* argv, so a `ps` sampled during the spawn can catch them; the exposure drops from hours to milliseconds. The `LEO_API_TOKEN` agents hold is also the narrower agent token — rejected at `/login` and on the config editor. See [Authentication](../configuration/config-reference.md#authentication).

None of this is a sandbox. Agents run as your user, so anything in `~/.leo/leo.yaml` is reachable by an agent that goes looking, and `tmux show-environment` still reports session env. What these measures remove is the *incidental* copy — the one that lands in a transcript because a routine call returned it.

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

- **Channel plugin:** if your plugin exposes an agent-list command, it shows running agents with stop buttons
- **Web UI:** agents panel on the dashboard
- **CLI:** `leo agent list` (`--json` for scripting)
- **API:** `GET /api/agent/list`

### Attaching

- **CLI:** `leo agent attach <name>` — full tmux attach, same TUI as running Claude locally. Works remotely via `ssh -t <host> tmux attach -t leo-<name>` when `client.hosts` is configured.

### Stopping

- **Channel plugin:** tap the stop button next to an agent (if the plugin exposes one)
- **Web UI:** stop button in the agent panel
- **CLI:** `leo agent stop <name>`
- **API:** `POST /api/agent/stop {"name": "..."}`

Stopping any agent — shared-workspace or worktree — kills its tmux session but always keeps the record, the stored claude session id, and (for a worktree agent) the on-disk worktree. Stop never deletes anything; the agent goes dormant and can be reattached (`leo agent start` or just `leo agent attach`, which starts it first) or inspected later.

To remove an agent for good — its record, and a worktree agent's checkout and branch — stop it, then run `leo agent delete <name>` (`--delete-branch` to also drop the branch). `delete` refuses a live agent.

### Deleting

- **CLI:** `leo agent delete <name>` (`--delete-branch` for worktree agents, `--yes` to skip the confirmation prompt)
- **Attach picker:** `D`, with a confirm naming exactly what will be removed
- **API:** `DELETE /agents/{name}`

There is no MCP tool for deletion — agents cannot delete agents. Deleting is a human-operator action only. See [Permissions](../configuration/permissions.md).

### Switching templates

An agent can be re-pointed at a different template without losing the project it is working in:

```bash
leo agent set-template leo-coding-owner-fetch codex
```

The agent keeps its name, workspace, and worktree; everything else — harness, model, permissions, env — is rebuilt from the target template. In `leo attach`'s picker, **t** opens the same chooser over the selected agent.

Conversations are per template. The session the agent had on the template it leaves is archived on its record, and switching back hands it over again; a template it has never run starts fresh. Because the archive is keyed by template name rather than harness, a `coding` and a `review` template both on claude keep separate conversations — switching between them is a way to keep two threads on one project, not just a way to change harness.

Two things a switch deliberately does not do: rename the agent (stable names keep tmux sessions, channel routing, and scripts working) and move it to the target template's `workspace`. Agents backing a `runtime: persistent` task are refused, since those bind to their agent by name — change `tasks.<name>.template` instead.

## Session Naming

Agents are named based on the template and repo. A repo-less spawn is the one exception — the name is just the template name, with no `leo-` prefix or repo segment:

| Input | Agent Name |
|-------|------------|
| `leo agent spawn coding` (no repo) | `coding` |
| `/agent coding owner/repo` | `leo-coding-owner-repo` |
| `/agent coding my-project` | `leo-coding-my-project` |
| `leo agent spawn coding --repo owner/repo --worktree feat/cache` | `leo-coding-owner-repo-feat-cache` |

Worktree spawns append a sanitized branch slug so two agents on different branches of the same repo don't collide. Long slugs are truncated with a short content hash to stay within filesystem-friendly length bounds.

This name is used as the `--name` flag for Claude, so it appears exactly as shown in claude.ai and the Claude app.

If a name collides with an existing agent, Leo appends `-2`, `-3`, etc.

## Persistence

Agent records are stored in `~/.leo/state/agents.json`. When the daemon restarts, it checks if each agent's tmux session is still alive and re-registers surviving sessions with the supervisor. A dormant (stopped) agent is skipped at restore — it stays dormant until started or, for a failed-restore record, retried — and its worktree, if it has one, stays on disk so you can `leo agent delete` it later. Restore also runs `git worktree prune` against each canonical clone so git's admin metadata stays consistent with the filesystem.

## Supervisor Behavior

Every agent — spawned directly or as the implicit/explicit target of a `runtime: persistent` task — is supervised the same way:

- Auto-restart on exit with exponential backoff
- Quick-exit detection (< 15s) clears stale sessions
- Resume prompt auto-dismissal (the "Resume from summary" prompt is handled automatically)
