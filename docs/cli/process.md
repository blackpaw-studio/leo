# leo process

Manage supervised processes.

## Usage

```bash
leo process list                          # list configured processes and their runtime state
leo process list --json                   # list as JSON for scripting
leo process add <name>                    # add a new process (flags or interactive)
leo process remove <name>                 # remove a process from the config (prompts for confirmation)
leo process enable <name>                 # enable a disabled process
leo process disable <name>                # disable a process without removing it
leo process attach <name>                 # attach to the process's tmux session
leo process logs <name> [-n LINES] [-f]   # tail the process's pane output
```

Process names support shell tab-completion for `remove`, `enable`, `disable`, `attach`, and `logs`.

Both `attach` and `logs` accept `--host NAME` to target a remote leo server via SSH — see the [Remote CLI guide](../guides/remote-cli.md) for host configuration.

## Subcommands

### `leo process list`

Shows each configured process with its model, enabled state, workspace, and channels. If the daemon is running, runtime state (status, restart count) is appended in brackets:

```
assistant           sonnet   enabled  [running]
    workspace: /Users/you/.leo/workspace
    channels:  plugin:telegram@claude-plugins-official
coding              opus     disabled
    workspace: /Users/you/agents/coding
```

### `leo process add <name>`

Adds a new process. If no flags are given, prompts interactively. Otherwise uses the flag values.

**Flags:**

| Flag | Description |
|------|-------------|
| `--workspace <path>` | Process workspace directory (blank = default) |
| `--channels <csv>` | Comma-separated channel plugin IDs |
| `--model <model>` | Model override (defaults to global default) |
| `--agent <id>` | Agent identifier |
| `--disabled` | Create in a disabled state (default: enabled) |

Example:

```bash
leo process add research --workspace ~/workspaces/research --model opus
```

If the daemon is running, Leo reminds you to run `leo service restart` for the change to take effect.

### `leo process remove <name>`

Removes the named process from the config. Reminds you to restart the service if the daemon is running so the process is stopped.

Prompts for confirmation by default. Pass `-y / --yes` to skip the prompt (required when stdin is not a TTY).

### `leo process enable <name>` / `leo process disable <name>`

Toggles the process `enabled` flag. Disabled processes are skipped by `leo service start`. If the daemon is already running you'll need to restart it for the change to apply.

### `leo process attach <name>`

Attach to the process's tmux session. Leo keeps all supervised sessions on a
dedicated tmux socket — every invocation passes `-L leo`, so `leo-<name>`
sessions never mix with your personal tmux server.

- **From a normal shell:** Leo replaces the CLI with `tmux -L leo attach -t leo-<name>` via `syscall.Exec` so the TUI owns the TTY cleanly.
- **From inside tmux:** Leo uses `display-popup -E` on your outer tmux server to open the leo session as an overlay, preserving your original tmux session when the popup is dismissed (no nested tmux).
- **Remotely:** Leo runs `ssh -t <host> tmux -L leo attach -t leo-<name>` (using the host's configured `tmux_path`).

Pass `--cc` to open the session in tmux control mode (`-CC`), which iTerm2
and WezTerm pick up as a native tab. Control mode is refused cleanly from
inside tmux or over SSH.

Detach with the normal tmux prefix + `d` (default: `C-b d`). The process
keeps running under the supervisor. See [tmux config](../guides/tmux-config.md)
for deeper detail on the dedicated socket and recommended bindings (tmux
3.2+).

### `leo process logs <name>`

Capture the tmux pane for the named process.

- `-n/--lines N` — tail length (default 200)
- `-f/--follow` — stream via `tail -f` on a temp log file fed by `tmux pipe-pane`. Ctrl-C to exit.

Both modes honor `--host` and the host's `tmux_path` override when running against a remote.

## See Also

- [`leo service`](service.md) — start/stop/restart supervised processes
- [`leo attach`](attach.md) — shortcut that dispatches to `process attach` or `agent attach`
- [Remote CLI guide](../guides/remote-cli.md) — host setup and SSH walkthrough
- [Config Reference](../configuration/config-reference.md#processes) — full process field specification
