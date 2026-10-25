# Startup-stall repro (Phase B)

`run.sh <records> <burn-procs> [port]` starts an isolated daemon (own `LEO_HOME`, `HOME`,
`TMUX_TMPDIR`, web port) over `<records>` synthetic dispatch records from `gen.py`, waits for
`/health` ready on its socket, prints the `startup:` timing lines, then kills the daemon, burn
processes and tmux server. `DUMP_AFTER=<s>` runs `leo service dump` that many seconds in.
Needs `bin/leo` (`make build`). Never touches `~/.leo`, launchd, or the production tmux socket.

Note: the `lock` step line goes to stderr only (the log rotator isn't installed yet); the rest are in `state/service.log`.
