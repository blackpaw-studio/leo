# Managed install (Leo.app)

Leo.app, the macOS app, can bundle the leo daemon and tmux and run them as its
own LaunchAgent. When it does, leo is **managed**: the app owns the daemon's
registration, binary path and tmux binary, and leo defers to it. This page
describes the contract between leo and the app. Nothing here applies to a
plain CLI install (brew or curl), which behaves as it always has.

A managed install is macOS-only. On Linux a marker is ignored with a warning.

## The marker

The app writes `~/.leo/state/managed.json` (under the resolved leo home's
`state/`, so isolated homes work too), atomically. Every leo invocation, CLI
and daemon, reads it.

```json
{
  "version": 1,
  "manager": "Leo.app",
  "bundle_id": "studio.blackpaw.leo.macos",
  "launchd_label": "studio.blackpaw.leo.daemon",
  "self_path": "~/.leo/app/bin/leo",
  "tmux_path": "~/.leo/app/tmux/3.6a/tmux",
  "env": { "PATH": "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin" }
}
```

| Field | Meaning |
|-------|---------|
| `version` | Must be `1`. |
| `manager`, `bundle_id` | Who owns the install. `manager` is shown in `leo status` and `leo doctor`. |
| `launchd_label` | The app's LaunchAgent. Reverse-DNS, and never `com.blackpaw.leo` (the CLI install's own label). |
| `self_path` | The stable path leo persists wherever it records its own binary (see below). `~` is expanded. |
| `tmux_path` | The pinned tmux, laid out `<root>/<version>/tmux`. `~` is expanded. |
| `env` | Optional string-to-string map the daemon applies to its own environment at startup, before it launches anything. Agents inherit it, and it overrides inherited values. |

### Validation

A marker that fails any check is ignored: leo behaves as unmanaged, prints one
warning per process to stderr (and `leo doctor` repeats it), and never refuses
to run.

- The file is valid JSON with `version` 1; unknown fields are ignored.
- `self_path` and `tmux_path` are absolute after `~` expansion, and both exist.
- `tmux_path` looks like `<root>/<version>/tmux`.
- `launchd_label` is reverse-DNS and is not `com.blackpaw.leo`.
- `env` keys are non-empty with no `=` or NUL; values have no NUL. One bad
  entry invalidates the whole marker.
- The file is not group- or world-writable: it controls a path that ends up in
  hooks agents execute.
- The platform is macOS.

## What changes when managed

| Command or place | Managed behaviour |
|------------------|-------------------|
| Persisted leo paths (`leo-mcp.json`, codex `hooks.json` and `config.toml`, the tmux prefix+L binding, `LEO_BRIDGE_BIN`) | Use `self_path` instead of the running executable, so an app update never leaves a stale path behind. |
| tmux | `tmux_path` is used for the daemon and the CLI (see [the tmux guard](#the-tmux-version-guard)). |
| `leo update` | Refuses: `leo is managed by Leo.app; update the app (Leo ▸ Check for Updates)`. This includes `--check`. Independently of the marker, a leo binary inside `*.app/Contents/` never replaces itself, since editing a signed bundle breaks its code signature. |
| `leo service start`, `stop` (and `--daemon`) | Refuse, naming Leo.app and the launchd label. The app owns registration, and its `KeepAlive` would undo a CLI stop. |
| `leo service restart` | Runs `launchctl kickstart -k gui/<uid>/<launchd_label>`, never touching `com.blackpaw.leo`. The web UI's restart button and `leo web enable` go through the same path. |
| `leo setup` | Skips the daemon install with an info line. |
| `leo status` | Reports the manager and the label, and the status of that job. |
| `leo doctor` | Reports the managed state, the job, `self_path` and where its symlink points, `tmux_path` against the version its directory claims, the tmux resolution (including a pending upgrade), and a loaded `com.blackpaw.leo` job that would race the managed daemon. |

The managed LaunchAgent runs exactly `<bundle>/Contents/Helpers/leo service
--supervised`, with no `--config`: the daemon resolves `~/.leo` itself and logs
to `state/service.log`.

## First start

Leo.app registers its LaunchAgent on a clean account before anything has run
`leo setup` or installed the Claude CLI, and the app's Claude onboarding needs a
live daemon. The managed daemon therefore starts without either:

- **No config.** When the marker is valid and the default config
  (`~/.leo/leo.yaml`) does not exist, the daemon writes the setup wizard's
  default config (an `assistant` template and the default model and turn
  limit) before loading it, and logs `managed install: no leo.yaml found;
  created default config at …`. The file is `0600` and a missing `~/.leo` is
  created `0700`. The write is atomic and never replaces a file: an existing
  config, or one that appears concurrently, is used as is. Unmanaged installs
  keep exiting with `no leo.yaml found — run 'leo setup' first`, and an
  explicit `--config` is never created.
- **No `claude`.** A missing Claude CLI never stops the daemon (managed or
  not). It is looked up again each time something needs it, so installing it
  needs no restart. Until then:
  - `GET /health` on the socket reports `"claude": {"found": false}`, and
    `{"found": true, "path": "/…/claude"}` once it resolves. The lookup uses
    the marker's `env.PATH` first, like every other binary.
  - Spawning a claude-harness agent fails with `claude CLI not found; install
    it from https://claude.com/claude-code`. Where a JSON surface exists the
    error code is `claude_not_found` (HTTP 424 on `/api/agent/spawn` and the
    socket's `/agents/spawn`). Agents on other harnesses are unaffected, and a
    claude agent restored at boot is kept as stopped (`leo agent restart` once
    claude is installed).
  - Scheduled and persistent tasks that need claude fail per run with the same
    message; the daemon keeps running.
  - `leo doctor` reports `Claude Code: not found`.

## The tmux version guard

tmux clients and servers must be the same version. The app pins one tmux per
directory, `<root>/<version>/tmux`, and never removes a version directory a
running server may still use.

If a `-L leo` server is running with version V and `tmux_path` is a different
version, leo uses `<root>/V/tmux` for every command (daemon and CLI) and logs
`tmux upgrade pending`; `tmux_path` is used only when a **new** server starts,
for example after a reboot. leo never `kill-server`s to resolve a mismatch.
When no copy matching the server's version exists, leo logs an error and falls
back to `tmux_path`; attaching may fail, and `leo doctor` reports it. The CLI
stays silent about a pending upgrade.

## One daemon per home

The daemon takes an exclusive `flock` on `state/daemon.lock` before it touches
`leo.sock`, the log or tmux. A second daemon for the same home exits non-zero
with `another leo daemon owns <home> (pid N)`. This holds for managed and
unmanaged installs alike.

While both the CLI job and the managed job are registered, the loser respawns
about every 10 seconds under `KeepAlive`. It is noise, not harm; `leo doctor`
flags it.

## Handover

`leo service handover` moves the daemon between the CLI install and the app.
The app calls it; it is not meant for hand use. Both directions drain busy
agents first (the [graceful restart](../cli/service.md#leo-service-restart)
drain), then change registration while the tmux server and its sessions stay up
(`launchctl bootout` of a leo job leaves the foreground tmux server running),
so the new daemon adopts the agents.

```bash
leo service handover --to-app --label studio.blackpaw.leo.daemon --json
leo service handover --to-cli --json
```

| Flag | Description |
|------|-------------|
| `--to-app` | Hand the daemon to Leo.app. Needs `--label`, the launchd label the app registers. |
| `--to-cli` | Release the daemon back to the CLI install. |
| `--json` | One JSON object on stdout; progress on stderr; failure exits non-zero. |
| `--dry-run` | Run the read-only checks and print the plan. Changes nothing. |
| `--drain-timeout`, `--force` | As for `leo service restart`. |

**`--to-app`** checks that there is no marker, that the label is valid and
differs from the CLI install's own, and that the CLI plist exists. It records
the plist's `EnvironmentVariables` (the app's LaunchAgent can carry them),
drains, boots out the CLI job, moves the plist to `<plist>.handed-over`, and
waits until `daemon.lock` is free. The app then writes `managed.json` and
registers its LaunchAgent. A CLI daemon whose plist exists but whose job is not
loaded skips the drain and bootout; only the plist moves. A daemon running with
no launchd job at all fails `no_launchd_job`; "running" means it holds `daemon.lock`, not that its socket answers, so a daemon with broken IPC is never mistaken for a stopped one.

**`--to-cli`** checks the marker, the `.handed-over` backup, and that the
backup's `ProgramArguments[0]` exists and is executable. It drains through the
managed daemon, boots out the marker's job, waits for the lock, moves
`managed.json` to `managed.json.releasing`, restores the plist, bootstraps it,
waits until the CLI daemon is ready, and deletes `managed.json.releasing`. The
marker moves aside before the CLI daemon starts: with it live, the new daemon
would persist the app's `self_path` everywhere. If the restored binary is older
than the one doing the handover, the result carries a warning and the release
continues.

**`--to-cli` without a marker (recovery).** If `--to-app` succeeded but the app
then failed to write `managed.json`, the CLI job is already booted out and its
plist sits at `<plist>.handed-over`, with no managed job to release. With no
marker (absent, not merely invalid) and that backup present, `--to-cli` needs
`--label <the app's launchd label>` (leo cannot know it without the marker;
missing is `invalid_args`, and it is validated like `--to-app`'s). It then
skips the drain and the bootout and fails `managed_job_loaded` if
`launchctl print gui/<uid>/<label>` finds the app's job still registered (a
crashed or restarting app job would otherwise run beside the restored CLI
job; the app must unregister it first). It fails `daemon_running` if a daemon
holds `daemon.lock`, validates the backup's `ProgramArguments[0]` as above,
moves the backup back to the plist, bootstraps it and waits until the daemon
is ready. The result is `ok: true` with `managed_stopped: false` and
`restored_without_marker: true`. A failure moves the plist back to
`.handed-over` (after a best-effort bootout of the CLI job, in case launchd
registered it in part) and reports `rolled_back`. With no marker and no backup
the error is still `not_managed`; an invalid marker is never treated as
absent. `--dry-run` prints the plan for this path too.

### Result

```json
{
  "ok": true,
  "previous_label": "com.blackpaw.leo",
  "env": { "PATH": "/opt/homebrew/bin:/usr/bin:/bin" },
  "plist_backup": "/Users/me/Library/LaunchAgents/com.blackpaw.leo.plist.handed-over"
}
```

`previous_label` is the job that was stopped: the CLI label for `--to-app`, the
marker's label for `--to-cli`. `env` is only set by `--to-app`. A failure is
`{"ok": false, "step": …, "error": …, "message": …}` plus whatever applies of:

| Field | Meaning |
|-------|---------|
| `rolled_back` | Present only when an undo was attempted. `true`: the files and job were restored. `false`: the undo failed too, so the previous state was **not** restored. |
| `managed_stopped` | Always present (`false` unless set). `--to-cli` only. The managed job was booted out and leo cannot re-register an app-owned job, so the app must register its LaunchAgent again. |
| `restored_without_marker` | `--to-cli` recovery only: the restore ran with no marker, so no managed job was stopped. |
| `warnings` | Non-fatal notes (an older restored binary, a leftover `.releasing` file). |
| `plan` | With `--dry-run`: the steps that would run. |

Steps: `preflight`, `env`, `drain`, `bootout`, `plist`, `lock`, `marker`,
`bootstrap`, `ready`. Error codes, all part of the contract: `unsupported`,
`already_managed`, `invalid_label`, `label_conflict`, `no_launchd_job`,
`plist_missing`, `env_unreadable`, `drain_timeout`, `drain_failed`,
`bootout_failed`, `plist_move_failed`, `lock_timeout`, `not_managed`,
`backup_missing`, `backup_unusable`, `marker_move_failed`,
`plist_restore_failed`, `bootstrap_failed`, `not_ready`, `daemon_running`, `managed_job_loaded`, `config_error`, plus `invalid_args` and `not_permitted` for bad flags and denied agents.

## The JSON rule

Every `--json` command (`service restart`, `service handover`, `pair`,
`web enable`) follows one rule:

- stdout carries only the JSON object; logs and progress go to stderr;
- a failure is `{"error": "<stable code>", "message": "…"}` (message optional)
  with a non-zero exit. That includes bad flags and arguments, which answer
  `invalid_args`.

```bash
leo service restart --json
# {"ok":true,"drained":false,"restarted":true}
```

`leo service restart --json` returns exit 0 for a daemon that is down but whose
job is loaded (`drained: false`), `not_registered` (exit 1) when the job is not
loaded, and `drain_timeout` (exit 1) when busy agents do not pause in time and
`--force` is not set. Other codes: `restart_failed`, `drain_failed`,
`config_error`.

## Repair

| Symptom | Fix |
|---------|-----|
| `warning: managed.json ignored: …` | Fix the named field, or let the app rewrite it (the app shows "repair"). leo runs unmanaged in the meantime. |
| `leo update` / `service start` refuse but you removed the app | Delete `~/.leo/state/managed.json`. |
| Two daemons fighting (`another leo daemon owns …` in the log, respawning) | `launchctl bootout gui/$(id -u)/com.blackpaw.leo` after a handover, or bootout the app's label. `leo doctor` names the one still loaded. |
| `tmux upgrade pending` | Nothing to do; the next server start uses the pinned version. |
| `doctor` reports `tmux_path … sits in directory …` | The app copied a different tmux into that directory; have the app re-copy it. |
| Agents lose LAN access after a takeover | The adopted tmux server's creator is gone; [`leo service reparent`](../cli/service.md#leo-service-reparent) repairs it, bouncing agents. |
| A failed `--to-cli` left the app's agent stopped (`managed_stopped: true`) | Have the app register its LaunchAgent again. |
| Moving Leo.app out of `/Applications` | The `~/.leo/app/bin/leo` symlink goes stale. Move it back; the daemon keeps running from the old path until then. |
| Remote attach to a managed Mac | The SSH paths run `host.tmux_path` (default `tmux`), which is not the bundled one. Set `tmux_path` on the host entry. |
