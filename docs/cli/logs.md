# leo logs

Show the service log, or filter it for a specific supervised agent or session.

## Usage

```bash
leo logs [name] [-n <lines>] [-f]
```

## Description

Tails the daemon/service log stored under `~/.leo/state/`. Passing a name filters the stream to lines emitted by that agent or persistent session.

Name arguments support tab-completion (see [`leo completion`](completion.md)).

## Flags

| Flag | Description |
|------|-------------|
| `-n, --tail <N>` | Number of lines to show from the tail. Defaults to 50. |
| `-f, --follow` | Stream new output as it arrives. |

## Examples

```bash
# Last 50 service log lines
leo logs

# Last 200 lines for a specific agent
leo logs coding-assistant -n 200

# Follow an agent's log in real time
leo logs coding-assistant -f
```

## See Also

- [`leo agent logs`](agent.md) — per-agent tmux scrollback
- [`leo task logs`](task.md) — per-task execution history
