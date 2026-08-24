# leo attach

Top-level shortcut for [`leo agent attach`](agent.md).

## Usage

```bash
leo attach [name] [--host <host>] [--cc]
```

## Description

Leo resolves `<name>` against the set of running agents (spawned via `leo agent spawn` or a template) and attaches to its tmux session. If no agent matches, Leo returns an error.

Omitting the name opens a full-screen, fuzzy-filterable picker over every agent — local and every configured remote host — in every state, with in-place lifecycle actions (attach, stop, start, delete, rename, set template). See [`leo agent attach`](agent.md#leo-agent-attach-name) for the full key list.

When `--host` targets a remote, Leo delegates the whole resolution to the server by shelling `ssh -t <host> leo attach <name>` — the client does not need to know the remote's agent list.

## Flags

| Flag | Description |
|------|-------------|
| `--host <name>` | Target a remote host defined under `client.hosts` in the config. |
| `--cc` | Render the session as a native tab via tmux control mode. Requires a tmux-aware terminal (iTerm2, WezTerm). |

## Examples

```bash
# Pick interactively from the local daemon
leo attach

# Attach to a running agent by name
leo attach coding-assistant

# Target a specific remote host from client.hosts
leo attach fetch --host prod

# Render as a native tab in a tmux control-mode-aware terminal
leo attach coding-assistant --cc
```

## See Also

- [`leo agent attach`](agent.md#leo-agent-attach-name) — explicit form for ephemeral agents
- [Remote CLI guide](../guides/remote-cli.md) — configuring `client.hosts`
