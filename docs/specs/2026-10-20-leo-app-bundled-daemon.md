# Leo.app bundles the leo daemon

Status: draft · 2026-10-20 · Owners: leo agent (leo side), leoterm agent (app side)

## Goal

A user downloads Leo.app, drags it to /Applications, and has a working local
daemon with agents — no curl, no brew, no Terminal. Remote daemons keep working
exactly as today. A Mac can also run as a headless "server" from the same app.

## Decisions (agreed with Evan)

- Repos stay separate. leoterm embeds a **pinned leo release**; no monorepo.
- **Lockstep updates.** Each app release pins one leo version. A leo release
  fires `repository_dispatch` → automated bump PR in leoterm → merge cuts a
  Sparkle release. Sparkle (EdDSA) is the only trust root for the bundled binary.
- **Existing installs: offer to take over.** If a brew/curl daemon is live, the
  app uses it and offers once: "Let Leo.app manage the daemon?" Takeover is
  reversible ("Release to CLI").
- **Server is a mode of Leo.app**, not a second app: menu bar extra +
  "Run as server only" + "Pair a device…" (QR via leo's existing pairing).
- Claude CLI is never bundled; onboarding installs/logs in via a Leo terminal tab.
- The `/api/v1` + chat wire contract schema is a **separate spec** (not here).

## Architecture

```
Leo.app/Contents/
  Helpers/leo               pinned leo release (arm64+amd64 lipo), signed nested
  Helpers/tmux              pinned tmux (static, signed nested)
  Library/LaunchAgents/studio.blackpaw.leo.daemon.plist   (SMAppService.agent)

~/.leo/app/                 the managed root (no spaces in any path)
  bin/leo        -> /Applications/Leo.app/Contents/Helpers/leo   (refreshed each launch)
  tmux/<ver>/tmux   copied from Helpers/tmux, never overwritten
~/.leo/state/managed.json   the managed-install marker (written by the app)
```

The managed LaunchAgent's `ProgramArguments` are exactly
`[<bundle>/Contents/Helpers/leo, service, --supervised]` with no `--config`: the
daemon resolves the default leo home (`~/.leo`) itself, and logs to
`state/service.log` (the daemon's own log rotator; the plist sets no
`StandardOutPath`).

The daemon uses the default leo home (`~/.leo`), so the CLI, web UI, iOS and the
app all see the same agents and config.

### Managed-install marker — the leo ⇄ app contract

`~/.leo/state/managed.json`, written atomically by the app, read by every leo
invocation (CLI and daemon):

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

`env` is optional (string → string). The daemon applies it to its own process
environment at startup, before it launches anything, so agents inherit it and it
overrides inherited values. It is validated like the other fields: keys must be
non-empty and contain no `=` and no NUL; values must contain no NUL. An invalid
`env` makes the whole marker invalid (unmanaged plus a warning).

Absent file = today's behaviour, unchanged. Malformed / unknown `version` = leo
logs a warning and behaves as unmanaged (never refuses to run).

## leo-side requirements (leo agent)

1. **Marker loading.** One package reads/validates `managed.json` (`~` expanded,
   paths must be absolute after expansion and exist; `env` entries valid; else
   ignored with warning). The daemon applies `env` to its own process environment
   at startup (see the marker section).
2. **Stable self path.** When managed, every place that *persists* leo's path
   (launchd plist, codex `hooks.json`/`config.toml`, `leo-mcp.json`, tmux
   prefix+L binding, bridge `LEO_BRIDGE_BIN`, migrate rewrites) uses
   `self_path` instead of `os.Executable()`. Runtime-only uses may keep
   `os.Executable()`.
3. **tmux path.** Resolution order becomes: managed `tmux_path` → existing
   lookup. Applies to daemon and CLI (`leo agent attach` must talk to the same
   tmux binary as the server).
4. **tmux server version guard.** Copies live at `<root>/<ver>/tmux` where
   `<root>` = `dirname(dirname(tmux_path))` and `<ver>` is `tmux -V` output
   minus the `tmux ` prefix (e.g. `3.6a`). The app builds/pins tmux (≥3.2) and
   never removes a version dir a running server may use. If a `-L leo` server is running
   with version V ≠ `tmux_path`'s version and `<root>/V/tmux` exists, leo uses
   that binary (daemon and CLI) and logs "tmux upgrade pending"; `tmux_path` is
   used only when starting a new server (e.g. after reboot). leo never
   `kill-server`s to resolve a mismatch. If no matching copy exists, leo logs an
   error and falls back to `tmux_path` (attach may fail; doctor reports it).
5. **Self-update defers.** `leo update` when managed: exit non-zero with
   "leo is managed by Leo.app; update the app (Leo ▸ Check for Updates)". No
   download, no binary replacement. Independently of the marker, any leo binary
   under `*.app/Contents/` (e.g. `Contents/Helpers/leo`) refuses to replace itself:
   editing a signed bundle would break its code signature.
6. **Service commands respect the manager.** When managed:
   - `leo service install|uninstall|start|stop` refuse with a message naming
     Leo.app (the app owns registration; KeepAlive would undo a CLI stop).
   - `leo service restart` (incl. graceful drain) acts on
     `gui/<uid>/<launchd_label>` via `launchctl kickstart -k`, never on
     `com.blackpaw.leo`.
   - `leo service restart --json` (JSON rule below): a daemon that is down but
     whose job is loaded exits 0 with `{ok:true, drained:false, restarted:true}`;
     a job that is not loaded exits 1 with `not_registered`; a drain timeout
     without `--force` exits 1 with `drain_timeout`.
   - `leo doctor` reports managed state, label, self/tmux paths.
7. **Single daemon per home.** The daemon takes an exclusive `flock` on
   `state/daemon.lock` before touching `leo.sock`; a second daemon exits non-zero
   with "another leo daemon owns <home> (pid N)". This fixes the existing
   socket-unlink race regardless of managed mode.
8. **Takeover / release primitives** (invoked by the app via the CLI, JSON out):
   - `leo service handover --to-app --label <l> --json`: drain (graceful-restart
     drain path), boot out `com.blackpaw.leo`, move its plist aside to
     `<plist>.handed-over`, report `{ok, previous_label, env}`. The app then
     writes `managed.json` and registers its LaunchAgent; agents are adopted
     from the surviving tmux sessions.
     - A CLI daemon whose plist exists but whose job is not loaded is also
       supported: skip the drain and the bootout, just move the plist aside.
     - A running daemon with no launchd job (started by hand or with a pid
       file) fails preflight with `error=no_launchd_job`.
     - If a step fails and the rollback itself fails too, the result carries
       `rolled_back:false` (with the failing `step`), so the app knows the old
       state was not restored.
   - `leo service handover --to-cli --json`: inverse; restores the plist,
     bootstraps it, and removes `managed.json` only after the CLI daemon is up.
   - Captured env from the old plist is returned so the app's LaunchAgent can
     carry it (the app plist otherwise sets a minimal explicit env + PATH).
   - **JSON rule** for every `--json` command (handover, `service restart`,
     `pair`, `web enable`): stdout carries only the JSON object; logs and
     progress go to stderr; a failure is `{error: <stable code>, message?}` with
     a non-zero exit. `<stable code>` values are part of the contract (e.g.
     `no_launchd_job`, `not_registered`, `drain_timeout`, `web_disabled`).
9. **Pairing for the menu bar.** `leo pair --json` prints the existing
   `devices.Payload` plus `warnings[]` instead of a QR; if the web listener is
   off it exits non-zero with `{error: "web_disabled"}`. `leo web enable
   [--bind <addr>] --json` sets `web.enabled` (and bind) and triggers a graceful
   restart.
10. **Release artifact.** goreleaser additionally publishes
    `leo_<ver>_darwin_universal.tar.gz` (lipo'd, notarized) and fires
    `repository_dispatch` (`leo-release`, payload `{version, sha256}`) to
    `blackpaw-studio/leoterm`.
11. Docs: `docs/configuration/managed-install.md` describing the marker and
    managed behaviour for external users.

## App-side requirements (leoterm agent — summary, they own the plan)

- Embed `Helpers/leo` + `Helpers/tmux` from the pinned release in `build-app.sh`;
  sign nested inside-out in `sign-package.sh`; `LEO_VERSION` pin file;
  bump-PR workflow on `repository_dispatch`; dev override `LEO_BINARY=…`.
- Require /Applications (move prompt) before registering anything.
- First run: probe `leo.sock` → live daemon → use it + one-time takeover offer;
  no daemon and no other install → copy tmux, write `managed.json`, refresh
  `bin/leo` symlink, register `SMAppService.agent`, wait for `hello`.
- `LeoCLI` candidate order: managed `self_path` first when managed.
- Onboarding: detect `claude`; offer official installer + `claude` login in a
  terminal tab.
- "Install `leo` command": symlink `~/.local/bin/leo` → `bin/leo` (confirm
  before replacing an existing file there).
- Menu bar extra: status, agents, restart (graceful), logs, Run as server only
  (accessory policy, no quit-on-last-window), Pair a device… (`leo pair --json`
  → QR; offers `leo web enable` on `web_disabled`), Release to CLI.
- After a Sparkle update: on launch, if bundled leo version ≠ daemon `hello.version`,
  trigger graceful restart; copy a new tmux version alongside old ones.

## Error handling

- Marker invalid → unmanaged behaviour + warning (leo); app shows "repair".
- Handover failure at any step → leo restores the previous plist and reports
  `{ok:false, step, error}`; the app keeps the CLI daemon and does not write
  `managed.json`.
- App moved/renamed out of /Applications → symlink refresh fails → app blocks
  with "move Leo back to Applications" (daemon keeps running from the old path
  until then; the symlink target is the only thing that breaks).

## Testing (leo side)

- Unit: marker parse/validate; self-path selection at each persist site; tmux
  resolution order; version-guard choice; `update` refusal; service command
  routing by label; flock contention (two daemons, injected lock); handover
  step rollback with faked launchctl — **assert exact argv** (see
  feedback on mocked exec seams).
- e2e (`make e2e`, isolated env): managed marker in a temp home → daemon writes
  `self_path` into plist/mcp config; second daemon refused.
- Live: isolated test daemon (separate LEO_HOME) handover to-app/to-cli with a
  hand-registered LaunchAgent; never against production without Evan's go.

## Out of scope

- `/api/v1` + chat JSON Schema contract (own spec).
- Bundling the Claude CLI; Linux packaging changes; public-URL setup
  (Tailscale/Cloudflare); iOS changes.
