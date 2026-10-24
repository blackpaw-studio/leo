# leo service

Manage the leo daemon.

## Usage

```bash
# Run the daemon in the foreground
leo service

# Background with auto-restart
leo service start
leo service stop
leo service status
leo service restart
leo service logs
leo service dump

# OS-level daemon (launchd/systemd)
leo service start --daemon
leo service stop --daemon
leo service status --daemon
```

## Description

`leo service` runs the leo daemon: the web UI, the cron scheduler, the daemon IPC server, and supervision for ephemeral agents — including the agents backing `runtime: persistent` tasks. On start it restores any agents that were running before the daemon last stopped (see `RestoreAgents`), then keeps each of them alive in its own tmux session with restart-on-crash and exponential backoff.

`leo service` no longer manages config-declared "processes" — agents (spawned via `leo agent spawn`, a template, or ensure-exists'd by a persistent task firing) are the only thing it supervises.

## Subcommands

When [Leo.app manages the install](../configuration/managed-install.md), `start`, `stop` (and their `--daemon` forms) refuse with a message naming Leo.app and its launchd label, and `restart` acts on that label.

### `leo service start`

Starts the daemon in the background with automatic restart on crash for supervised agents. Uses exponential backoff (5s initial, 60s max) to avoid rapid restart loops.

**Flags:**

| Flag | Description |
|------|-------------|
| `--daemon` | Install as an OS service (launchd on macOS, systemd on Linux) instead of a simple background process. Persists across reboots. |

### `leo service stop`

Stops the running daemon, then tears down every agent's tmux session by exact name. Session IDs are preserved so a subsequent start resumes where each agent left off.

`stop` is the only thing that kills agent sessions. The daemon itself never does on shutdown: a restart (any form, including a [graceful restart](#leo-service-restart)), a [handover](#leo-service-handover), a crash, a launchd or systemd respawn, and a bare `SIGTERM` (for example Leo.app unregistering its daemon) all leave the sessions running for the next daemon to adopt. The generated systemd unit sets `KillMode=process` for the same reason, so systemd does not kill the tmux server with the daemon; reinstall with `leo service start --daemon` to pick that up on an existing install.

**Flags:**

| Flag | Description |
|------|-------------|
| `--daemon` | Remove the OS service instead of stopping a background process. |

### `leo service status`

Shows whether the daemon is currently running.

**Flags:**

| Flag | Description |
|------|-------------|
| `--daemon` | Check OS service status instead of background process status. |

### `leo service restart`

Restarts the daemon. If agents are mid-turn, leo can let them reach a safe
stopping point first (a *graceful restart*) and tell them to resume afterwards.

1. **Busy check.** leo asks the daemon which agents are working: a bridged
   agent by its mod's busy flag, any other agent by reading its composer the
   way message delivery does. It also lists the interactive dispatches with a
   turn running. With no busy agents the restart proceeds at once, as before;
   when only dispatches are running, leo says so and restarts without asking.
2. **Prompt.** Otherwise it asks:

   ```
   2 agents are working (assistant, plex) and 1 dispatch is running (fix-x). Graceful restart? [Y/n]
   ```

   `--graceful` and `--no-graceful` answer it in advance. A non-interactive
   run without either flag restarts at once, as before. Dispatches are never
   paused: the new daemon adopts their live panes (see
   [Dispatches → Daemon restarts](../configuration/dispatches.md#daemon-restarts)).
3. **Drain.** Each busy agent gets an urgent `[leo]` message asking it to stop
   starting new work, save or commit what it has, note where it is, and end
   its turn. A bridged agent receives it at its next turn boundary; any other
   agent once its composer frees up (retried until delivered). Agents that
   were not busy are left alone. Cron firings due during the drain are
   deferred, not dropped: they run once each after the restart (or right away
   if the restart is cancelled).
4. **Wait.** leo polls until every drained agent has received the message and
   gone idle, printing each agent's progress (`notified`, `drained`). An agent
   that goes busy again is waited on again.
5. **Restart.** leo restarts the daemon. Before it dies, the daemon writes the
   drain set to `state/pending-resume.json` (mode 0600). The new daemon, once
   it has restored its agents, sends each one still around
   `[leo] Daemon restarted. Resume where you left off.` and deletes the file.
   The daemon owns this step, so resume happens even if the CLI died
   mid-restart.

When `--drain-timeout` (default `10m`) runs out, an interactive run lists the
agents still working and offers `[r]estart anyway / [w]ait another 10m /
[a]bort`. A non-interactive run aborts with a non-zero exit, unless `--force`
is set, which restarts anyway (`--force` also skips the interactive question).
Aborting, pressing Ctrl-C, or a restart that fails cancels the drain: the
paused agents get `[leo] Restart cancelled. Resume your work.`, deferred cron
firings run, and `pending-resume.json` is removed. If the CLI disappears
without cancelling (killed, or its SSH session dropped), the daemon cancels
the drain itself after a minute without a status poll.

The web UI's restart button always drains: it runs
`leo service restart --graceful --force`, so it never blocks on a prompt.

A daemon older than this feature has no drain support; restarting it (for
example on the first `leo update` to a version that has it) prints a warning
and restarts at once.
If no daemon is running there is nothing to drain, and leo restarts at once.
If the busy check fails for any other reason, `--graceful` aborts with a
non-zero exit rather than restart without draining (add `--force` to restart
anyway), an interactive run without a flag asks before restarting, and
`--no-graceful` or an unattended run without a flag restart with a warning.

A single turn longer than the timeout always reaches it: the pause message
cannot cut a running turn short, only stop the agent from starting new work
once it ends. Dispatch runs are neither messaged nor waited on; the calling
agent's drain covers the parent turn.

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `--graceful` | `false` | Drain busy agents without asking. |
| `--no-graceful` | `false` | Restart at once even if agents are busy, without asking. |
| `--drain-timeout` | `10m` | How long to wait for busy agents to pause (at most `24h`). |
| `--force` | `false` | When the drain times out, restart anyway instead of asking (interactive) or aborting (non-interactive). |
| `--json` | `false` | Print one JSON object on stdout (see below); progress goes to stderr. Never asks a question. |

When [Leo.app manages the install](../configuration/managed-install.md), the restart is `launchctl kickstart -k gui/<uid>/<launchd_label>` for the marker's label, never `com.blackpaw.leo`.

With `--json`, stdout is `{"ok":true,"drained":false,"restarted":true}` on success (`drained` is true when busy agents were paused first; `warnings` appears if the daemon restarted but was not ready in time). A daemon that is down but whose launchd job is loaded restarts and exits 0. Failures are `{"error":"<code>","message":"…"}` with exit 1: `not_registered` (no job is loaded, so nothing was done), `drain_timeout` (busy agents did not pause and `--force` was not set; they were told to resume), `restart_failed`, `drain_failed`, `config_error`.

```bash
$ leo service restart --graceful
Asked 2 busy agent(s) to pause; waiting up to 10m.
  assistant: waiting
  plex: notified
  plex: drained
  assistant: notified
  assistant: drained
Restarting daemon (waiting for agents to restore)...
Daemon restarted (running (pid 41022)).
```

### `leo service reparent`

Recycles leo's tmux server so the **currently running** daemon is its parent.

Leo deliberately adopts a tmux server that outlived a previous daemon — that is
what keeps agent sessions alive across `leo update` and `leo service restart`,
neither of which recreates the server. On macOS the cost is that Local Network
access for agent panes is attributed to the leo process that *created* the
server; once that process has exited the grant can lapse, and third-party
binaries under agents lose LAN access (`EHOSTUNREACH` on every LAN host) while
leo's own checks still pass. [`leo doctor`](doctor.md) detects this by probing
from inside the tmux tree, and `leo status` shows who owns the server.

This is the repair, and it is deliberately manual: an automatic respawn would
fire on every `leo update` and bounce every agent. It terminates every live
agent session — workspaces, agent definitions, and session ids survive, but
in-flight conversation context does not — then waits up to 30s for the daemon's
supervise loop to start a fresh, owned server.

**Flags:**

| Flag | Description |
|------|-------------|
| `--yes` | Skip the confirmation prompt. |
| `--force` | Recycle even when the server's owner is confirmed alive (otherwise it reports there is nothing to repair and exits). |

```bash
$ leo service reparent
tmux tree: server pid 32086, adopted — creating leo process (pid 58688) has exited; Local Network attribution is no longer verifiable
Recycle the tmux server? This terminates 15 live agent session(s) and 25 dormant one(s) will restart on next use [y/N]: y
tmux server killed; waiting for the daemon to start a fresh one...
new tmux server pid 41022, owned by live leo pid 33666
```

### `leo service handover`

Moves the daemon between the CLI install and Leo.app. Called by the app, not by hand: `--to-app --label <label>` drains agents, boots out the CLI's launchd job and moves its plist aside; `--to-cli` reverses it. Both support `--json` and `--dry-run`. See [Managed install](../configuration/managed-install.md#handover) for the steps, rollback behaviour and JSON shape.

### `leo service logs`

Tail the daemon log file.

**Flags:**

| Flag | Default | Description |
|------|---------|-------------|
| `-n`, `--tail` | `50` | Number of lines to display. |
| `-f`, `--follow` | `false` | Follow the log output (like `tail -f`). |

### `leo service dump`

Asks the running daemon to write every goroutine's stack to `<home>/state/goroutines-<unix>.txt` (mode 0600) and prints that path. The daemon keeps serving; nothing about its state changes. Use it on a daemon that is slow to start or looks stuck, to see where it is. A second dump in the same second gets a `-2`, `-3`, … suffix. Dump files are not pruned; remove them by hand.

Under the hood this sends `SIGUSR1`, whose default action is to terminate a process. So the command takes the daemon's PID from its `/health` answer on the socket (never from the lock file, which can be stale), and signals only a daemon whose `/health` carries `"goroutine_dump": true`. A daemon that does not answer exits non-zero with `daemon is not running`; one that predates the feature is refused with a message naming its PID and version. If no file appears within 10 seconds the command exits non-zero and points at `leo service logs`.

## Claude Arguments

For each supervised agent, Leo builds `claude` arguments based on its config:

```
claude --channels <channels>               \
       --add-dir <workspace>               \
       --add-dir <extra-dirs...>           \    # if add_dirs configured
       --remote-control <name>             \    # if harness_options.remote_control enabled
       --dangerously-skip-permissions      \    # if harness_options.bypass_permissions enabled
       --mcp-config <mcp-config-path>      \    # if MCP servers exist
       --session-id <id> | --resume <id>        # session persistence
```

## Logs

All modes write logs to `~/.leo/state/service.log`. The daemon rotates this file automatically on size: when it reaches 10 MB, [lumberjack](https://pkg.go.dev/gopkg.in/natefinch/lumberjack.v2) renames it to a timestamped backup (`service-<timestamp>.log.gz`) and opens a fresh file in place. Up to 3 backups are retained for 30 days, gzipped. No external logrotate setup is required. `leo service logs -f` reopens cleanly across rotations.

### Startup timing

While starting, the daemon logs one timestamped line per step, `startup: <step> took <duration>`, covering the lock, tmux server, observability, chat open, daemon wiring, IPC listen, agent manager, web (with its `web.New`, `MarkInterrupted`, `SweepRunFiles`, `setupChat`, `StartChat/Recover` and `listener bind` sub-steps), `RestoreAgents`, and a final `startup: total took …` once the daemon reports ready. `MarkInterrupted` and `SweepRunFiles` also log how many records or run files they handled and how many tmux and git calls they made, and `MarkInterrupted` warns about any single record that takes more than 2 s. A step that never finishes never logs its line, so the last `took` line marks where startup is stuck; `leo service dump` shows the exact location.

## Service Labels

- **macOS (launchd):** `com.blackpaw.leo`
- **Linux (systemd):** `leo.service`

## See Also

- [Background Mode](../guides/background-mode.md) -- detailed comparison of background vs daemon mode
- [Configuration → Channels](../configuration/config-reference.md#channels) — wiring up a channel plugin
