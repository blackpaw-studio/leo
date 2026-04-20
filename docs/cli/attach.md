# leo attach

Top-level shortcut for [`leo process attach`](process.md) and [`leo agent attach`](agent.md).

## Usage

```bash
leo attach [name] [--host <host>] [--cc]
```

## Description

Leo resolves `<name>` against both the configured processes and the set of running agents:

- If only a configured process matches, Leo attaches to its tmux session.
- If only a running agent matches, Leo attaches to the agent's tmux session.
- If both match, Leo refuses to guess and asks you to use the explicit subcommand.
- If neither matches, Leo returns an error.

Omitting the name opens an interactive arrow-key picker over the available processes and agents (local or remote).

When `--host` targets a remote, Leo delegates the whole resolution to the server by shelling `ssh -t <host> leo attach <name>` — the client does not need to know the remote's process list.

## Flags

| Flag | Description |
|------|-------------|
| `--host <name>` | Target a remote host defined under `client.hosts` in the config. |
| `--cc` | Render the session as a native tab via tmux control mode. Requires a tmux-aware terminal (iTerm2, WezTerm). |

## Examples

```bash
# Pick interactively from the local daemon
leo attach

# Attach to a configured process or running agent by name
leo attach coding-assistant

# Target a specific remote host from client.hosts
leo attach fetch --host prod

# Render as a native tab in a tmux control-mode-aware terminal
leo attach coding-assistant --cc
```

## See Also

- [`leo process attach`](process.md#leo-process-attach-name) — explicit form for supervised processes
- [`leo agent attach`](agent.md#leo-agent-attach-name) — explicit form for ephemeral agents
- [Remote CLI guide](../guides/remote-cli.md) — configuring `client.hosts`
