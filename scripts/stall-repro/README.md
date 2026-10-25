# Startup-stall repro (Phase B)

`run.sh <records> <burn-procs> [port]` starts an isolated daemon (own `LEO_HOME`, `HOME`,
`TMUX_TMPDIR`, web port) over `<records>` synthetic dispatch records from `gen.py`, waits for
`/health` ready on its socket, prints the `startup:` timing lines, then kills the daemon, burn
processes and tmux server. `DUMP_AFTER=<s>` runs `leo service dump` that many seconds in.
Needs `bin/leo` (`make build`). Never touches `~/.leo`, launchd, or the production tmux socket.

`WAIT_FOR_LOG=<regex>` keeps the daemon up after ready until a matching line reaches
`state/service.log` (under `MAX_WAIT`), e.g. `WAIT_FOR_LOG='restart reconcile: [0-9]+ records'` for the
background restart reconcile's summary (a bare `restart reconcile:` also matches its slow-record and git
timeout warnings, which arrive before the pass is done). The runner also reports when the web UI first answered (polled every 0.2 s).

`snapshot.sh <T>` prints the end state a run left in `<T>` as sorted JSON (per record: status, worktree
state, whether a branch tip was recorded, pane/viewer ids, error, notification dispositions; plus the
repository's worktrees and `leo/*` branches and `state/settings`), with timestamps and commit hashes
dropped. `gen.py` is seeded (`--seed`, default 1), so two runs at the same count can be diffed.

Since Phase C the `lock` step line is logged after the log rotator is installed, so it is in
`state/service.log` with the rest.
