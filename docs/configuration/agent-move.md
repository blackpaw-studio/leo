# Moving and copying agents

`leo agent move` hands an agent off to another Leo host. It stops the agent here and resumes the same conversation, in the same workspace state, over there. `leo agent copy` forks an agent into an independent second agent, either on this host or on another one. The source keeps running and the two diverge from then on.

```bash
leo agent move web --to laptop          # hand off
leo agent move web --to dionysus        # from the laptop: bring it back
leo agent copy web                      # fork here as web-copy
leo agent copy web --to laptop --name web-laptop --branch try-sqlite
```

Moves are on demand and incremental. A move back sends only what changed since the last move, and the side that pushes is always authoritative.

## One live home per agent

After a move, the source keeps a dormant record with `moved_to: <host>`. `leo agent list` and the web UI show it. Leo refuses to start, wake or deliver to that record, persistent-task deliveries included, and points at the destination instead. Moving the agent back from the destination clears the marker.

A copy is never marked. It is a new agent with its own name, workspace, branch and session.

## Setting up the hosts

Each host must name the other in `client.hosts`. A move runs `leo` on the destination over ssh, and the destination needs a route back for the return trip:

```yaml
# on dionysus
client:
  default_host: localhost       # required
  hosts:
    laptop:
      ssh: evan@laptop.lan
      leo_path: /Users/evan/.leo/app/bin/leo
```

```yaml
# on the laptop
client:
  default_host: localhost       # required
  hosts:
    dionysus:
      ssh: evan@dionysus.lan
```

**`default_host: localhost` is required on both hosts.** Without it, a plain `leo agent …` on a host that has `client.hosts` is forwarded to a remote host. That includes the receive verbs a move runs on the destination, so the move would go to a third host.

Both hosts also need:

- **The same home path and username.** A move keeps every path as-is, and a mismatch is refused (`home_mismatch`, `user_mismatch`).
- **Non-interactive ssh** from the source to the destination: key auth, no prompts. Leo runs ssh with `BatchMode=yes`.
- **rsync** on both hosts. macOS's `/usr/bin/rsync` (openrsync) works. Mixing openrsync on one end with GNU rsync on the other has not been tested.
- **The agent's template and every environment it uses,** under the same names. Leo does not copy templates or environments, so a missing one is refused (`template_missing`, `environment_missing`). Fix it on the destination: use the [shared templates file](config-reference.md#sharing-templates-between-machines-templates_file) or add it by hand. The destination's version of the template is used as-is.
- **The harness's session root on the destination,** for example the environment's `CLAUDE_CONFIG_DIR` or `CODEX_HOME`, already logged in (`session_root_missing` otherwise).

Every path that rsync or git sees must match `^/[A-Za-z0-9._@%+=,/-]+$`, because openrsync has no `--protect-args`. A path with spaces is refused as `unsafe_path`.

## Usage

### `leo agent move <name> --to <host>`

| Flag | Meaning |
|---|---|
| `--to <host>` | Destination, a `client.hosts` entry. Required. |
| `--force` | Move a busy agent anyway, cutting its turn off. Without it, an agent mid-turn is refused (`agent_busy`). |
| `--json` | Print the finished job as JSON instead of progress. |
| `--host <host>` | Run the move from another Leo host (the usual remote-CLI flag). |

### `leo agent copy <name>`

| Flag | Meaning |
|---|---|
| `--to <host>` | Destination. Omit it to copy on this host, which uses no ssh. |
| `--name <name>` | The copy's name. Defaults to `<name>-copy`, then `-copy-2` and so on, skipping names either host already has. |
| `--branch <branch>` | The copy's git branch. Defaults to `<current branch>-<copy name>`. Required when HEAD is detached. |
| `--json`, `--host` | As for move. |

The move runs as a job in the source daemon, so interrupting the command does not stop it. The command prints each stage as it starts (`validate`, `preflight`, `stop`, `pre-send`, `rsync-workspace`, …, `commit`, `mark`), then any notes, then where the agent now lives:

```
moved web to laptop (laptop.lan)
  rsync-workspace: 12 changes
  rsync-sessions: 3 changes
  rsync-staging: no changes
attach with: leo --host laptop agent attach web
```

Each rsync stage (`rsync-workspace`, `rsync-sessions`, `rsync-staging`, `rsync-modules`) reports how many changes it made. With `--json` the result carries them in `rsync`, one entry per stage in the order they ran: `{"stage", "total", "lines", "truncated"}`. `lines` are rsync's `--itemize-changes` lines (including deletions), capped at 200 per stage; `truncated` counts the rest.

### MCP and HTTP

- `leo_agent_move {name, to, force?}` and `leo_agent_copy {name, to?, new_name?, branch?}` start the job and return it at once. An agent that moves itself loses its MCP server partway through, and the daemon finishes the move anyway.
- The same job starts from `POST /api/agent/{name}/move` with `{"mode": "move"|"copy", "to", "force", "name", "branch"}`. It answers 202 with the job, or 409 with an error `code`.

### Permissions

- Move is gated by `leo_agent_move`.
- Copy is a spawn. It needs `leo_agent_copy`, needs `leo_spawn_agent` not to be denied, and needs the source agent's template in `can_spawn`.
- These gates apply to the CLI as well as MCP. See [Permissions](permissions.md).

## What travels

| Part | How |
|---|---|
| Workspace | rsync over ssh with `--delete` (the source wins), uncommitted and untracked files included, minus `move.exclude`. In a plain repo, `.git` travels too. |
| Conversation | The harness's session files for this agent: Claude's transcript and sidecars, or Codex's rollouts. They go with `--update` and no delete, so a newer file on the destination is never overwritten. A store directory on the destination may be a symlink (a shared `projects/` tree): the destination resolves each path to its real directory first (`session-targets`, move protocol 2) and the files are rsynced straight into it, so a symlink is never replaced. opencode sessions go by `opencode export` / `import` through staging. |
| Project memory | Claude's `projects/<encoded path>/memory/`, sent with `--update` and no delete. |
| Hook staging | `LEO_MOVE_DIR`, carried from `pre_send` to `post_receive`. |
| Agent record | Name, template, environments, session id, repo and branch. The destination creates or updates its record and starts the agent, which resumes the session. |

## Git

**Plain mode.** A repo with no linked worktrees on either host is rsynced whole, `.git` included.

**Git mode.** Leo switches to git mode when the workspace is a linked worktree (a `leo agent worktree` agent), or when its repo has linked worktrees on either host. In git mode Leo never rsyncs a git dir:

- **Commits and refs** are pushed into `refs/leo-move/<agent>/*` in the destination's canonical clone.
- **The index** travels as a tree object.
- **Working files** are rsynced without `/.git`.
- **The worktree itself** is rebuilt with `git worktree add --no-checkout`.

The branch is fast-forwarded with a compare-and-swap. If it has moved on independently, the move is refused (`branch_diverged`) before anything on the destination changes. A branch checked out in another worktree there is refused too (`branch_checked_out`).

If the destination has no canonical clone and the agent's repo is an `owner/repo`, the canonical is cloned with `gh` before anything stops. Otherwise the move is refused with the clone command to run (`canonical_missing`). A canonical whose history shares no root commit with the source's is refused (`canonical_mismatch`).

**Submodules** travel too. Each submodule's commits go by the same push, so a submodule commit that was never pushed upstream still arrives. A torn submodule (its gitlink and checkout disagree mid-operation) is refused (`submodule_torn`). A submodule URL that differs between the hosts, or points at a local path, gets a note.

**Other worktrees of the same repo nested inside the workspace** are skipped with a note (`nested_worktree_skipped`). Each one stays with its own agent, if it has one.

**What the destination already holds inside the workspace** is never deleted: another agent's workspace, a worktree of its repo, or a nested repository the source doesn't have. The workspace rsync excludes them. If the source has files at one of those paths, they are not sent, and a note (`dest_nested_kept`) names the path.

**Refused outright:**

- Shallow or sparse superprojects.
- A git dir laid out unlike `<repo>/.git` (`git_unsupported`).
- A merge, rebase, cherry-pick, revert or bisect in progress (`git_op_in_progress`).

**Not carried:** stashes (they are repo-wide), the reflog, `ORIG_HEAD`, intent-to-add and skip-worktree bits, and LFS objects of unpushed commits.

After a move, the source keeps its worktree and branch checkout under the dormant record. Moving back reuses them.

## Copies

- **Name and workspace.** The copy gets a new name and the workspace `<workspace>-<copy name>`. It never gets the source's path, even on another host, so a later move cannot collide with it.
- **Git.** The copy gets the branch `<branch>-<copy name>`, or `--branch`, created at the source's HEAD with the uncommitted changes as they are. Submodules on a branch get forked branches the same way. A branch that already exists is refused (`branch_exists`). A copy of an agent that works in its main checkout becomes a linked-worktree agent.
- **Conversation.** The copy's conversation is forked, not shared. Claude resumes with `--fork-session` and Codex forks its rollout, so each agent writes its own session from then on. A Claude agent can be copied mid-turn: the copy ends at the last completed turn. Codex and opencode agents can only be copied between turns, and `--force` does not change that. A harness that cannot fork refuses the copy (`harness_unsupported`).
- **Memory.** Claude's project memory is keyed by workspace path, so the copy gets its own memory dir, seeded from the source's.
- **Persistent tasks.** A copy of an agent backing a `runtime: persistent` task is not wired to the task, and the result notes that.

## Configuration: `move:`

The global block and a template's block combine. Lists concatenate, global first. A template's `hook_timeout` replaces the global one.

```yaml
move:
  exclude: [node_modules/, .build/]
  hook_timeout: 5m          # per hook; default 10m
  hooks:
    pre_send: [~/bin/mem-export]       # on the source, after the agent stops
    post_receive: [~/bin/mem-import]   # on the destination, before it starts

templates:
  web:
    move:
      exclude: [dist/]
```

- `exclude` patterns are rsync excludes for the workspace. An excluded path is never sent, and it is never deleted on the destination.
- `exclude` and `pre_send` are read on the source. `post_receive` is read on the destination, from the destination's own config.

### Hooks

Each hook entry is an absolute path (`~/` is expanded). It is exec'd directly, with no shell and no arguments, in the agent's workspace, and the hooks of a phase run in order. A hook that exits nonzero or outlives `hook_timeout` aborts the move, and the error carries the tail of its output. On a timeout, Leo kills the hook's whole process group.

A hook's environment is the daemon's environment, then the agent's resolved env (minus its Leo API token and opencode config), then:

| Variable | Value |
|---|---|
| `LEO_MOVE_MODE` | `move` or `copy` |
| `LEO_AGENT`, `LEO_WORKSPACE`, `LEO_SESSION_ID` | The agent. On a copy's `post_receive`, these are the new agent's. |
| `LEO_SOURCE_AGENT`, `LEO_SOURCE_WORKSPACE`, `LEO_SOURCE_SESSION_ID` | The source agent. |
| `LEO_MOVE_FROM`, `LEO_MOVE_TO` | Source and destination hostnames (`os.Hostname()`). |
| `LEO_MOVE_DIR` | A staging dir. Whatever `pre_send` writes there is in `post_receive`'s copy. |
| `LEO_MOVE_LAST` | RFC 3339 time of the previous move of this agent between this pair of hosts. Empty on the first move and on every copy. |

Use hooks for state Leo does not know about, such as a memory store keyed outside the workspace.

## Failure and recovery

Preflight runs before anything stops. It checks the destination's reachability, home, user, template, environments, harness binary, session root, name, workspace and git state, and it reports every problem at once:

```
move refused:
  - template_missing: laptop has no template "web"
  - environment_missing: laptop has no environment "acct-b"
```

After the agent stops:

- **Anything that fails before the destination is running** leaves the source stopped but not marked moved. The error says so. `leo agent start <name>` brings it back here, and re-running the move is safe because every transfer is incremental.
- **A failure after the destination has placed the agent** leaves a dormant shadow there, marked as moved back to the source. The source stays unmarked.
- **Right before the final start, the source fences the agent.** It records `move_pending` (destination, run id and time) on its record in `state/agents.json`. The fence survives a daemon restart. While it holds, nothing here starts, restarts, resets, wakes, moves, copies or rewires the agent. That includes task delivery, chat delivery and boot restore. Each refusal (`move_pending`) names the destination and how to settle the move. Only a settled outcome lifts the fence. Either the agent is marked moved, or the destination confirms that the commit failed and was rolled back (phase `failed`, agent not running), which makes the agent startable here again. A refusal whose rollback did not finish there keeps the fence (`outcome_unknown`).
- **If the destination does not answer the final start,** Leo asks it what it holds and how far that transfer's commit got (the destination records each phase in `state/moves/<name>/commit.json`). If the agent is running there, the move completes with a note (`commit_answer_lost`). If the commit failed there and left the agent dormant, the fence lifts and the agent here can be started again. Otherwise the commit may still be running hooks or starting the agent (`outcome_unknown`): the agent stays fenced here, and the error says to check `leo --host <to> agent list`.
- **If marking the source moved fails** after the destination is live, the agent stays fenced here.

A failed copy leaves `<workspace>-<name>.leo-partial` behind. The next run with the same name reuses it.

Only one move or copy of an agent runs at a time (`agent_moving`).

### Settling a pending move

`leo agent move <name> --reconcile` asks the destination how the fenced run's commit ended:

- **Running there, or delivered and since stopped:** the agent is marked moved here.
- **Failed there and left dormant:** the fence lifts, and `leo agent start <name>` recovers the agent here.
- **Still in progress, no record of the run, or no answer:** the agent stays fenced (`outcome_unknown`). Retry later.

The daemon runs the same reconcile for every fenced agent when it starts, in the background, and logs each outcome.

If the destination is gone for good (wiped, decommissioned, never coming back), `leo agent move <name> --abandon-remote` lifts the fence here without asking it. It prompts first; `--yes` skips the prompt, and without a TTY `--yes` is required.

> **Warning:** `--abandon-remote` cannot know whether the agent is running on the destination. If it is, starting it here leaves two live copies of one conversation, each writing its own history. Use it only when the destination will not come back, and prefer `--reconcile` whenever the destination can answer.

### Preflight codes

| Code | Meaning |
|---|---|
| `protocol_mismatch` | The destination's Leo speaks a different move protocol. Update one side. |
| `home_mismatch`, `user_mismatch` | Different home path or username. |
| `template_missing`, `environment_missing` | Create it on the destination. |
| `harness_mismatch`, `harness_binary_missing` | The destination's template uses another harness, or its binary is missing. |
| `harness_unsupported` | The harness cannot move (or, for a copy, fork) a session. |
| `session_root_missing` | The destination lacks the harness's config dir. |
| `session_missing` | The source has no transcript for the agent's resume session (the message names the path searched). Nothing was stopped; the destination would otherwise resume another session of the same workspace. |
| `name_taken`, `workspace_in_use`, `workspace_exists` | The destination already has that name or path. |
| `unsafe_path`, `unsafe_ref` | A path or ref outside the safe character set. |
| `canonical_missing`, `canonical_mismatch` | No usable canonical clone on the destination. |
| `branch_checked_out`, `branch_diverged`, `branch_exists` | Git branch conflicts (see Git). |
| `git_missing`, `git_unsupported`, `git_op_in_progress`, `submodule_torn` | Git state Leo will not move. |
| `agent_moved` | The agent already lives elsewhere. Move it from there. |
| `move_pending` | An earlier move of the agent has not settled. Run `leo agent move <name> --reconcile` first. |
| `agent_busy` | Mid-turn. Retry, or pass `--force` (move only). |
| `same_host` | A move's `--to` reaches this host. |
