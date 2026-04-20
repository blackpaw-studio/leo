# leo agent

Spawn and control ephemeral Claude agents on a leo server. The `leo` binary is dual-purpose: on a server it supervises processes and runs scheduled tasks, and on a laptop it becomes a thin remote client that talks to a leo host over SSH.

## Usage

```bash
leo agent list                                                     # list running agents
leo agent spawn <template> --repo <owner/repo>                     # spawn from a template
leo agent spawn <template> --repo <name> --name <n>                # spawn with a custom name
leo agent spawn <template> --repo <owner/repo> --worktree <branch> # spawn into a dedicated git worktree
leo agent attach <name>                                            # attach to the agent's tmux session
leo agent session-name <query>                                     # print the tmux session name
leo agent stop <name> [--prune]                                    # stop a running agent (optionally remove worktree)
leo agent prune <name>                                             # remove a stopped worktree agent's on-disk state
leo agent logs <name> [-n LINES] [-f]                              # tail the agent's pane output
```

`<name>` for `attach`, `stop`, and `logs` accepts shorthand — see [Shorthand Resolution](#shorthand-resolution) below. `prune` takes the canonical name only (stopped agents don't appear in the resolver). `session-name` is the explicit resolver.

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

Spawn a new agent from the named template. `--repo` is required and takes either an `owner/repo` pair (Leo clones via `gh repo clone`) or a plain name (Leo reuses the template's configured workspace path).

```bash
leo agent spawn coding --repo blackpaw-studio/leo
leo agent spawn coding --repo my-app --name scratch
```

`--name` overrides the auto-derived name. When the agent already exists, Leo appends a numeric suffix (`-1`, `-2`, …) so repeated spawns never collide.

#### Worktree Spawns

Pass `--worktree <branch>` to isolate the agent in its own git worktree instead of sharing a single clone:

```bash
leo agent spawn coding --repo blackpaw-studio/leo --worktree feat/cache
leo agent spawn coding --repo blackpaw-studio/leo --worktree feat/new --base main
```

- `--worktree` requires `owner/repo` (slashless repos have no canonical clone to branch from).
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

### `leo agent attach <name>`

Attach to the agent's tmux session. Leo keeps all supervised sessions on a
dedicated tmux socket — every invocation passes `-L leo`, so `leo-<name>`
sessions never mix with your personal tmux server.

- **From a normal shell:** Leo replaces the CLI with `tmux -L leo attach -t leo-<name>` via `syscall.Exec` so the TUI owns the TTY cleanly.
- **From inside tmux:** Leo uses `display-popup -E` on your outer tmux server to open the leo session as an overlay, preserving your original tmux session when the popup is dismissed (no nested tmux).
- **Remotely:** Leo runs `ssh -t <host> tmux -L leo attach -t leo-<name>`.

Running `leo attach` without a name opens an arrow-key picker over
processes, agents, and (for remote hosts) sessions from `tmux -L leo
list-sessions`. Auto-attaches when exactly one session exists.

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
