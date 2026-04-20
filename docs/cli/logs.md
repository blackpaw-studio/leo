# leo logs

Show the service log, or filter it for a specific supervised process.

## Usage

```bash
leo logs [process-name] [-n <lines>] [-f]
```

## Description

Tails the daemon/service log stored under `~/.leo/state/`. Passing a process name filters the stream to lines emitted by that process.

Process-name arguments support tab-completion (see [`leo completion`](completion.md)).

## Flags

| Flag | Description |
|------|-------------|
| `-n, --tail <N>` | Number of lines to show from the tail. Defaults to 50. |
| `-f, --follow` | Stream new output as it arrives. |

## Examples

```bash
# Last 50 service log lines
leo logs

# Last 200 lines for a specific process
leo logs coding-assistant -n 200

# Follow a process's log in real time
leo logs coding-assistant -f
```

## See Also

- [`leo process logs`](process.md) — per-process tmux scrollback
- [`leo task logs`](task.md) — per-task execution history
