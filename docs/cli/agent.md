# leo agent

Spawn and control ephemeral Claude agents on a leo server. The `leo` binary is dual-purpose: on a server it supervises processes and runs scheduled tasks, and on a laptop it becomes a thin remote client that talks to a leo host over SSH.

## Usage

```bash
leo agent list                                                     # list running agents
leo agent spawn <template>                                         # spawn the template as-is (no repo)
leo agent spawn <template> --repo <owner/repo>                     # spawn from a template
leo agent spawn <template> --repo <name> --name <n>                # spawn with a custom name
leo agent spawn <template> --repo <owner/repo> --worktree <branch> # spawn into a dedicated git worktree
leo agent worktree <agent> <branch>                                # spawn a worktree agent branched off an existing agent
leo agent attach <name>                                            # attach to the agent's tmux session
leo agent session-name <query>                                     # print the tmux session name
leo agent stop <name> [--prune]                                    # stop a running agent (optionally remove worktree)
leo agent suspend <name>                                           # suspend a running agent (idle-suspend, manual)
leo agent resume <name>                                            # resume a suspended agent, rejoining its prior conversation
leo agent reset <name>                                             # stop, clear stored session id, and respawn fresh
leo agent prune <name>                                             # remove a stopped worktree agent's on-disk state
leo agent logs <name> [-n LINES] [-f]                              # tail the agent's pane output
```

`<name>` for `attach`, `stop`, `suspend`, `reset`, and `logs` accepts shorthand — see [Shorthand Resolution](#shorthand-resolution) below. `resume` and `prune` take the canonical name only — the shorthand resolver only matches *live* agents, and both a suspended agent (`resume`'s target) and a stopped worktree agent (`prune`'s target) are not live. `session-name` is the explicit resolver.

## Flags

Every `agent` subcommand accepts `--host NAME` to select a remote. The name must match an entry in `client.hosts` in `leo.yaml`, or the literal string `localhost` to force local dispatch even when remotes are configured.

Resolution order when `--host` is omitted:

1. `LEO_HOST` environment variable
2. `client.default_host` in `leo.yaml`
3. First entry in `client.hosts` (sorted by key)
4. Localhost (only if no hosts are configured)

If any hosts are configured, Leo assumes this machine is a client and will never silently fall back to localhost — pass `--host localhost` explicitly when you want to target the local daemon on a server.

## Host Configuration

Add a `client` section to `leo.yaml` on the client machine:

```yaml
client:
  default_host: prod
  hosts:
    prod:
      ssh: alice@leo.example.com
      ssh_args: ["-p", "2222"]
    dev:
      ssh: alice@devbox.local
```

`ssh` is passed verbatim as the SSH target. `ssh_args` adds extra flags (port, identity file, jump host) between the target and the remote command. Leo does not parse `~/.ssh/config` — anything SSH itself resolves works transparently.

See the [Remote CLI guide](../guides/remote-cli.md) for a complete walkthrough.

## Shorthand Resolution

Any subcommand that takes `<name>` — `attach`, `stop`, `logs` — accepts shorthand in place of the canonical agent name. `session-name` is the explicit resolver and accepts the same queries. Resolution walks these tiers in order and returns the first unambiguous match against live agents:

1. Exact full name (case-insensitive)
2. Exact stored repo (e.g. `owner/name`)
3. Repo short — the segment after `/` for `owner/name` repos, or the full value for slashless repos
4. Suffix match on the full name (`-<query>`)

```bash
leo agent stop leo          # resolves to leo-coding-blackpaw-studio-leo if unique
leo agent logs my-app -f    # matches either a bare-name agent or owner/my-app
```

Ambiguous queries print the candidates and exit non-zero — re-run with the full name or a more specific query. Only running agents participate; stopped records are never matched, so short names are reusable as soon as an agent stops.

The same resolver is shared by the daemon HTTP API (`POST /api/agent/stop`, etc.) and the web UI, so shorthand works identically everywhere.

## Subcommands

### `leo agent list`

Show running agents as a table:

```
NAME                TEMPLATE  WORKSPACE              STATUS   RESTARTS
leo-coding-my-app   coding    ~/agents/my-app        running  0
leo-scratch         -         ~/agents/scratch       running  1
```

`--json` emits the raw `AgentRecord` array for scripting.

### `leo agent spawn <template>`

Spawn a new agent from the named template. `--repo` (or the positional `[repo]` arg) is optional and takes either an `owner/repo` pair (Leo clones via `gh repo clone`) or a plain name (Leo reuses the template's configured workspace path under a per-name subdir). Omit it entirely to run the template as-is, directly in its own workspace — the agent is named after the template.

```bash
leo agent spawn coding                          # run the template as-is; agent named "coding"
leo agent spawn coding --repo blackpaw-studio/leo
leo agent spawn coding --repo my-app --name scratch
```

`--name` overrides the auto-derived name (the template name for a repo-less spawn, `leo-<template>-<repo>` otherwise). When the agent already exists, Leo appends a numeric suffix (`-2`, `-3`, …) so repeated spawns never collide.

#### Worktree Spawns

Pass `--worktree <branch>` to isolate the agent in its own git worktree instead of sharing a single clone:

```bash
leo agent spawn coding --repo blackpaw-studio/leo --worktree feat/cache
leo agent spawn coding --repo blackpaw-studio/leo --worktree feat/new --base main
```

- `--worktree` requires a repo, and specifically `owner/repo` (a repo-less or slashless spawn has no canonical clone to branch from).
- If the branch exists locally or on `origin`, Leo attaches to it. Otherwise Leo creates a new branch off `--base`, defaulting to origin's default branch.
- The worktree lives at `<baseWorkspace>/.worktrees/<repo-short>/<branch-slug>/`. See [workspace structure](../configuration/workspace-structure.md) for the full layout.
- The agent name includes the branch slug: `leo-<template>-<owner>-<repo>-<branch-slug>`.
- `leo agent list` shows a `BRANCH` column for worktree agents; stopped worktree agents stay in the list until you `prune` them.

#### Collision Prompt

When `--repo` is a bare name (no slash) that matches the short name of a running agent, Leo prompts for how to proceed:

- **a** — attach to the existing agent
- **b** — spawn a fresh agent using that agent's canonical `owner/repo`
- **c** — spawn a fresh agent under the template workspace (default)
- **q** — cancel

When `--repo` is `owner/repo` and a running agent already targets the same repo (and branch, if `--worktree` is set), Leo prompts with the same options minus **b** (since the user already supplied the canonical repo). Selecting **c** spawns a new agent with a numeric suffix (e.g. `-2`).

Non-TTY runs skip the prompt and default to fresh-template. Two flags override the prompt:

- `--attach-existing` — always attach if a collision is found
- `--reuse-owner` — always respawn using the existing canonical repo (slashless only)

On success Leo prints the resolved name and workspace, plus the one-liner to attach.

### `leo agent worktree <agent> <branch>`

Spawn a worktree agent branched off an existing agent — sugar over `spawn --worktree` that derives everything from the source agent's record instead of an `owner/repo`. Works for **any** agent whose workspace is a git repository, not just ones spawned with `--repo owner/repo`.

```bash
leo agent worktree chronicle a11y
leo agent worktree chronicle hotfix --base v1.2.0 --prompt "fix the crash"
```

- **Template and env are inherited** from the source agent's record by default; the source's workspace (or its canonical repo, if the source is itself a worktree agent) becomes the new agent's git canonical.
- **Naming:** `<agent>-<branch-slug>`. The worktree lives at `<workspace-base>/.worktrees/<agent>/<branch-slug>/`.
- **Branching off a worktree agent** uses that agent's canonical repo, not its own checkout — pass `--base <its-branch>` to fork from the branch it's on rather than the canonical's default.
- **Remoteless repos** (no `origin`): the fetch step is skipped and new branches are cut from `HEAD` instead of origin's default branch.
- If the branch exists locally or on `origin`, Leo attaches to it; otherwise it creates a new branch off `--base` (or the remoteless/default-branch fallback above).

Flags:

- `--name` — override the derived agent name
- `--base` — base ref for new branches (defaults to origin's default branch, or `HEAD` for remoteless repos)
- `--template` — build the new agent from a different template than the source agent's, keeping the same source repo as the git canonical; the override template must exist in config. Model, harness, and args re-resolve from it. **When set, none of the source agent's env is inherited** — only the override template's own env plus `--env`.
- `--prompt` — opening prompt delivered as the agent's first interactive turn
- `--env KEY=VALUE` — extra env var (repeatable); overrides an inherited value on collision
- `--json` — emit the spawned `AgentRecord` as JSON

Clean up the same way as any worktree agent: `leo agent stop <name> --prune`.

```bash
# chronicle is a running agent with a git workspace; branch it onto an a11y pass
leo agent worktree chronicle a11y
# spawned chronicle-a11y (branch: a11y, worktree: ~/.leo/workspace/.worktrees/chronicle/a11y)
# attach with: leo agent attach chronicle-a11y

leo agent stop chronicle-a11y --prune   # done — tear down the checkout
```

### `leo agent attach <name>`

Attach to the agent's tmux session. Leo keeps all supervised sessions on a
dedicated tmux socket — every invocation passes `-L leo`, so `leo-<name>`
sessions never mix with your personal tmux server.

- **From a normal shell:** Leo replaces the CLI with `tmux -L leo attach -t leo-<name>` via `syscall.Exec` so the TUI owns the TTY cleanly.
- **From inside tmux:** Leo uses `display-popup -E` on your outer tmux server to open the leo session as an overlay, preserving your original tmux session when the popup is dismissed (no nested tmux).
- **Remotely:** Leo runs `ssh -t <host> tmux -L leo attach -t leo-<name>`.

Running `leo attach` without a name opens a full-screen, fuzzy-filterable
picker over every agent — local and every configured remote host — in every
state (running, starting, suspended, stopped). Beyond attaching, the picker
doubles as a lifecycle surface: **Enter** attach (a suspended agent is resumed
first), **s** suspend, **u** resume, **x** stop (with confirmation), **r**
rename, **t** set template (arrow keys to choose, Enter to confirm, Esc to
cancel), **/** filter, **q** quit. The picker always opens when no name is
given — there is no longer a single-candidate auto-attach shortcut.

Pass `--cc` to open the session in tmux control mode (`-CC`), which iTerm2
and WezTerm pick up as a native tab. Control mode is refused cleanly from
inside tmux or over SSH.

`<name>` accepts shorthand — see [Shorthand Resolution](#shorthand-resolution).
Detach with the normal tmux prefix + `d` (default: `C-b d`). The agent keeps
running. See [tmux config](../guides/tmux-config.md) for deeper detail on the
dedicated socket and recommended bindings (tmux 3.2+).

### `leo agent session-name <query>`

Resolve a shorthand query to the canonical tmux session name and print it to stdout. Useful in scripts that want the canonical session string without attaching, and the building block for the remote attach round-trip:

```bash
tmux attach -t "$(leo agent session-name leo)"
```

### `leo agent stop <name>`

Stop a running agent. Kills the tmux session and deregisters from the supervisor. Accepts shorthand.

- Shared-workspace agents: the record is removed; the workspace stays on disk.
- Worktree agents: the record is preserved so you can reattach or inspect the branch. Pass `--prune` to also remove the worktree and record in a single round trip.

Flags (only meaningful with `--prune`, and only for worktree agents):

- `--prune` — also remove the on-disk worktree and agentstore record
- `--force` — with `--prune`, remove even when the worktree is dirty
- `--delete-branch` — with `--prune`, delete the local branch after the worktree is gone

### `leo agent suspend <name>`

Suspend a running agent: kills the process/tmux session but preserves the workspace and stored claude session id. Accepts shorthand. A suspended agent shows as `suspended` in `leo agent list` and auto-resumes on the next incoming message. See [Config Reference → Idle-suspend](../configuration/config-reference.md#idle-suspend).

### `leo agent resume <name>`

Resume a suspended agent, rejoining its prior conversation via `--resume`. Takes the canonical name only — shorthand resolution only matches live agents, and a suspended agent isn't one.

Like `restart`, resume re-applies today's defaults + template config (and the current binary's harness env) before resuming, so an agent suspended across an upgrade or a config edit wakes up current rather than replaying the wiring it was spawned with. Agents with no template, a deleted template, or a changed harness keep their stored args.

### `leo agent reset <name>`

Reset an agent to a brand-new conversation: stops any live process/tmux session, clears the stored claude session id, and respawns fresh from the agent's template. Accepts shorthand. Unlike `resume`, which rejoins the prior conversation, `reset` deliberately discards it — use this when an agent's context has gotten stuck or corrupted (a common case: a long-lived agent backing a `runtime: persistent` task whose conversation has filled up). See [Persistent Tasks → `leo agent reset`](../configuration/persistent-tasks.md#leo-agent-reset).

```bash
leo agent reset leo-coding-owner-fetch
```

### `leo agent set-template <name> <template>`

Re-point a running or suspended agent at a different template, keeping its name, workspace, and git worktree. Its harness, model, permissions, env, and the rest of its wiring are rebuilt from the target template. Accepts shorthand.

```bash
leo agent set-template leo-coding-owner-fetch codex
```

**Conversations are per template.** Switching away files the agent's current session under the template being left; switching back hands that conversation to it again. A template this agent has not run before starts fresh. The archive is keyed by template name, not harness, so two claude templates (a `coding` and a `review`, say) keep separate conversations.

```bash
leo agent set-template fetch codex     # coding → codex, new session
leo agent set-template fetch coding    # codex → coding, resumes where coding left off
```

The command reads lighter than it acts — a running agent is stopped and respawned — so its output always states what happened to the process and to the session. Pass `--json` for the machine-readable form (`from_template`, `to_template`, `from_harness`, `to_harness`, `resumed`, `status`).

A suspended agent is re-pointed in place, with no process to bounce; it comes up on the new template at its next resume.

Notes and limits:

- **The name is left alone**, even when it embeds the old template (`leo-coding-owner-fetch` running codex). Stable names keep tmux sessions, channel routing, and scripts working — rename it yourself with `leo agent rename` if you want it to match.
- **The target template's `workspace` is ignored.** The agent stays in the project it is working in, which is also what keeps its archived sessions valid.
- **Stopped agents are refused**, as are agents backing a `runtime: persistent` task — those bind to their agent by name, so switching one would redirect a scheduled task's prompts into a template it was never configured for. Change `tasks.<name>.template` instead.
- **Permissions:** a switch launches the target template, so it needs both `leo_stop_agent` and the `can_spawn` allowlist entry for that template. See [Permissions](../configuration/permissions.md).

### `leo agent prune <name>`

Remove the on-disk worktree and agentstore record for a worktree agent that has already been stopped. No-op (returns an error) for shared-workspace agents. Takes the canonical agent name — shorthand resolution only matches live agents, so `prune` requires the full name you saw in the last `leo agent list`. Use `leo agent stop --prune` instead when the agent is still running and you want shorthand.

```bash
leo agent prune leo-coding-blackpaw-studio-leo-feat-cache
leo agent prune feat-cache --delete-branch
```

Flags:

- `--force` — remove even when the worktree has uncommitted changes, or the branch is unmerged
- `--delete-branch` — delete the local branch after the worktree is gone

Typical flow:

```bash
leo agent stop feat-cache        # stop, leave worktree for inspection
# … review the branch, push a PR, merge …
leo agent prune feat-cache --delete-branch
```

Or in one step:

```bash
leo agent stop feat-cache --prune --delete-branch
```

### `leo agent logs <name>`

Capture the tmux pane for the named agent. Accepts shorthand.

- `-n/--lines N` — tail length (default 200)
- `-f/--follow` — stream via `tail -f` on a temp log file fed by `tmux pipe-pane`. Ctrl-C to exit.

## See Also

- [Remote CLI guide](../guides/remote-cli.md) — host setup and SSH walkthrough
- [Agents guide](../guides/agents.md) — templates, lifecycle, and channel/web parity
- [`leo template`](template.md) — manage the templates `agent spawn` consumes
