# Verify — Leo
Parallel builds: yes — tested 2026-10-13: `make build test` in this worktree and a throwaway second worktree at once, both green (46/46 packages each). Tests isolate HOME and config dirs per package (#258).

All commands run from the worktree root. Tested 2026-10-13 at 28154ec4.

## Build, lint, test
```
env -u TMUX make build        # → ./bin/leo
env -u TMUX make lint         # mirrors CI (pinned golangci-lint + gosec)
env -u TMUX make test         # go test -race -cover ./... ; 46 packages, all ok
go test -race -run TestName ./internal/<pkg>/   # single test
```
- Always `env -u TMUX`: a live `$TMUX` masks unstubbed exec seams in `internal/cli`, so tests pass locally and fail in CI.
- Known flakes (B-010): #247 `TestPaneLocationReadsTheLiveWindow`, #208 `TestAcceptDevChannelPromptTimeout`, #201 consult TempDir cleanup. Any other failure is real. Never rerun-and-move-on: a "flake" has been a real bug before.
- e2e hides behind a build tag (`go test ./...` skips it). Config/argv changes must also run, ONLY in this form:
  ```
  env -i PATH="$PATH" HOME="$HOME" USER="$USER" LANG=en_US.UTF-8 TMUX_TMPDIR="$(mktemp -d)" make e2e
  ```
  Any other form can leak into the live daemon and kill panes. Add `LEO_E2E_CLAUDE=1` only for the real-claude cases.

## Isolated daemon (CLI / tmux / supervisor behavior)
Own home, own tmux server (via `TMUX_TMPDIR`), own port. It never touches `~/.leo`, the launchd service, or the production tmux server and its roster lines.
```
H=$(mktemp -d /tmp/leoap.XXXX); mkdir -p $H/state     # short path: macOS caps AF_UNIX socket paths at ~104 bytes
printf 'web:\n  enabled: true\n  port: 8391\n  bind: 127.0.0.1\n' > $H/leo.yaml    # add templates/tasks/processes the flow needs
env -i PATH="$PATH" HOME="$HOME" LANG=en_US.UTF-8 TMUX_TMPDIR=$H nohup ./bin/leo service --supervised --config $H/leo.yaml > $H/daemon.log 2>&1 &
L() { env -i PATH="$PATH" HOME="$HOME" LANG=en_US.UTF-8 TMUX_TMPDIR=$H ./bin/leo -c $H/leo.yaml "$@"; }
L agent list                                  # → "No agents running."
tmux -S $H/tmux-$(id -u)/leo ls               # the isolated tmux server
```
Teardown (never `leo service stop|restart`; those hit PRODUCTION launchd regardless of config):
```
pkill -f "supervised --config $H/leo.yaml"; trash $H
```
- `-c $H/leo.yaml` is required on every CLI call: `LEO_HOME` does not isolate the CLI.
- Pick a free port if 8391 is taken (parallel lanes: always pick a free one); never 8370 (production).
- Agents spawned here run real claude/codex in `$HOME`; use a throwaway workspace under `$H` and cheap models. A new workspace shows claude's trust dialog: send `Down Enter` (bare Enter quits).
- Daemon restart test (B-001): `kill -9` the daemon PID only (SIGKILL, as `kickstart -k` does; SIGTERM shuts down gracefully and kills every session by design), start it again with the same command, compare `tmux -S … list-panes -F '#{pane_pid}'` before/after.

## Key flows
- Agent lifecycle: `L agent spawn <template> …`, `L agent list`, `L agent stop <name>`, kill + restart the daemon → agent adopted, pane PID unchanged.
- Persistent task: config `tasks.<t>.runtime: persistent`, `L run <t>` → prompt lands in the agent's pane (`tmux -S … capture-pane -p -t '=leo-<t>:1.1'`; quote `=` targets).
- Web config edit: log in, open Templates, edit a field, save → `$H/leo.yaml` shows the change as written.
- API: `curl -s -H "Authorization: Bearer $(cat $H/state/api.token)" http://127.0.0.1:8391/api/v1/...`.

## Isolated instance (web UI screenshots)
`autopilot-webshot` starts the app on a free loopback port with a fresh temp data dir, waits until it answers, drives a flow in headless Chromium, saves PNGs, then stops the app's whole process group and deletes only the temp dir it made. It never touches the live app or its data.
Run it with your normal `HOME` (Playwright's browser lives there); only the app is isolated.

Launch options used by every session below (the serve command seeds a known API token so flows can log in):
```
autopilot-webshot --serve 'mkdir -p {data}/state && printf "web:\n  enabled: true\n  port: %s\n  bind: 127.0.0.1\n" {port} > {data}/leo.yaml && printf %s 1111111111111111111111111111111111111111111111111111111111111111 > {data}/state/api.token && chmod 600 {data}/state/api.token && exec env -i PATH="$PATH" HOME="$HOME" LANG=en_US.UTF-8 TMUX_TMPDIR={data} ./bin/leo service --supervised --config {data}/leo.yaml' --cwd . --ready-path /login
```
To seed templates/tasks, append YAML to `{data}/leo.yaml` inside the serve command before `exec`.

Smoke check (run 2026-10-13; printed one `shot` line showing the Tasks page; nothing changed under `~/.leo`):
```
autopilot-webshot --serve '<serve command above>' --cwd . --ready-path /login \
  --flow - --out "$(mktemp -d)" --prefix smoke <<'FLOW'
goto /login?token=1111111111111111111111111111111111111111111111111111111111111111
wait 'text=Agents'
shot
FLOW
```

## Screenshots
For each web item, write a flow that logs in (first line above), drives the changed screen and shoots it, save it to a fresh `mktemp -d` dir, and run it with the launch options above:
```
autopilot-webshot --serve '<serve command>' --cwd . --ready-path /login \
  --flow <flow file> --out <shots dir> --prefix <item>
```
Steps: `goto /path` (the app's own origin only), `click SELECTOR`, `fill SELECTOR TEXT`, `press [SELECTOR] KEY`, `wait SELECTOR` (until visible), `shot`. Every flow needs a `shot`. Each shot is saved as `<prefix>-<n>.png` and printed as `shot <absolute path>`. Shoot after a `wait` for the state you want to show.

Exit codes: `0` ok; `1` app didn't start or a step failed (GUI failure; stderr has the log tail); `2` bad arguments/flow; `3` Playwright missing → report "not visually verified — Playwright unavailable: <stderr>".

For CLI/tmux items, evidence is text: the `L …` command output and `tmux -S … capture-pane -p` captures from the isolated daemon, saved under `shots/<item>/`.
