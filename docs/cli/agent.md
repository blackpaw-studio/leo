# leo agent

Spawn and control ephemeral Claude agents on a leo server. The `leo` binary is dual-purpose: on a server it supervises processes and runs scheduled tasks, and on a laptop it becomes a thin remote client that talks to a leo host over SSH.

## Usage

```bash
leo agent list                                        # list running agents
leo agent spawn <template> --repo <owner/repo>        # spawn from a template
leo agent spawn <template> --repo <name> --name <n>   # spawn with a custom name
leo agent attach <name>                               # attach to the agent's tmux session
leo agent session-name <query>                        # print the tmux session name
leo agent stop <name>                                 # stop a running agent
leo agent logs <name> [-n LINES] [-f]                 # tail the agent's pane output
```

`<name>` for `attach`, `stop`, and `logs` accepts shorthand — see [Shorthand Resolution](#shorthand-resolution) below. `session-name` is the explicit resolver.

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
      ssh: evan@leo.example.com
      ssh_args: ["-p", "2222"]
    dev:
      ssh: evan@devbox.local
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

When `--repo` is a bare name (no slash) that matches the short name of a running agent, Leo prompts for how to proceed:

- **a** — attach to the existing agent
- **b** — spawn a fresh agent using that agent's canonical `owner/repo`
- **c** — spawn a fresh agent under the template workspace (default)
- **q** — cancel

Non-TTY runs skip the prompt and default to fresh-template. Two flags override the prompt:

- `--attach-existing` — always attach if a collision is found
- `--reuse-owner` — always respawn using the existing canonical repo

On success Leo prints the resolved name and workspace, plus the one-liner to attach.

### `leo agent attach <name>`

Attach to the agent's tmux session. Locally, Leo replaces the CLI process with `tmux attach -t leo-<name>` via `syscall.Exec` so the TUI owns the TTY cleanly. Remotely, Leo runs `ssh -t <host> tmux attach -t leo-<name>`.

`<name>` accepts shorthand — see [Shorthand Resolution](#shorthand-resolution). Detach with the normal tmux prefix + `d` (default: `C-b d`). The agent keeps running.

### `leo agent session-name <query>`

Resolve a shorthand query to the canonical tmux session name and print it to stdout. Useful in scripts that want the canonical session string without attaching, and the building block for the remote attach round-trip:

```bash
tmux attach -t "$(leo agent session-name leo)"
```

### `leo agent stop <name>`

Stop a running agent. Kills the tmux session, deregisters from the supervisor, and removes the agent from the agentstore. Workspaces are left in place. Accepts shorthand.

### `leo agent logs <name>`

Capture the tmux pane for the named agent. Accepts shorthand.

- `-n/--lines N` — tail length (default 200)
- `-f/--follow` — stream via `tail -f` on a temp log file fed by `tmux pipe-pane`. Ctrl-C to exit.

## See Also

- [Remote CLI guide](../guides/remote-cli.md) — host setup and SSH walkthrough
- [Agents guide](../guides/agents.md) — templates, lifecycle, and Telegram/web parity
- [`leo template`](template.md) — manage the templates `agent spawn` consumes
