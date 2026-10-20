# Leo-side plan: Leo.app bundled daemon (spec items 1–11)

There are 4 PR-sized slices. Slice A goes first. B and C can run in parallel after A merges (C3 waits for B1). D has no dependencies and can start right away. Every test runs with `-race`. Fake exec seams record argv and assert the **exact** argv. Nothing uses real sleeps: retry and wait loops take an injected `sleep`/clock.

## Slice A: marker, single daemon, stable self path, update refusal (items 1, 2, 5, 7)

**A1. `internal/managed` package (new)**
- `type Marker {Version int; Manager, BundleID, LaunchdLabel, SelfPath, TmuxPath string}`.
- `type State {Marker; Managed bool; Warning string}`.
- `Load(stateDir string, d Deps) State`, where `Deps{ReadFile, Stat, Lstat, UserHome, GOOS}`.
- Path: `<cfg.StatePath()>/managed.json`. For the default home this is `~/.leo/state/managed.json`. Using the resolved home is what lets e2e and live tests run in isolated homes.
- Validation. Any failure means `Managed=false` plus a warning; leo never refuses to run.
  - File absent: unmanaged, no warning.
  - `version` must be 1. Unknown JSON fields are ignored.
  - `~` is expanded. Both paths must be absolute and exist.
  - Optional `env` (string to string): keys non-empty with no `=` and no NUL, values with no NUL; any violation invalidates the whole marker. `managed.ApplyEnv(env, setenv)` sets entries in key order; the daemon applies it first thing in `defaultSupervisedExec` through the `setenvFn` seam (`RunSupervisedOptions.Env`, filled by `runService`), so agents inherit it and it overrides inherited values.
  - `tmux_path` must look like `<root>/<ver>/tmux`.
  - `launchd_label` must be reverse-DNS and must not be `com.blackpaw.leo`.
  - The marker file must not be group- or world-writable, because it controls a path written into hooks.
  - GOOS must be darwin.
- `cli.managedState(cfg)` prints the warning to stderr once per process (`sync.Once`), behind the seam `loadManaged`.
- Failing test first: table-driven `managed/marker_test.go`, one row per rule above plus a valid row with expanded paths.

**A2. Single daemon per home (item 7)**
- New `internal/service/daemonlock.go`: `AcquireDaemonLock(stateDir) (release func() error, err error)`.
  - Uses `syscall.Flock(LOCK_EX|LOCK_NB)` on `state/daemon.lock`, then truncates the file and writes the pid.
  - Contention returns `*LockHeldError{Home, PID}`, whose message is "another leo daemon owns <home> (pid N)".
- Wire it as the **first** call in `defaultSupervisedExec` (process.go:883), before `installLogRotator`, `findTmux` and `EnsureForegroundServer`. A losing daemon then never rotates the log, touches tmux, or unlinks `leo.sock`.
- Tests:
  - Real flock on `t.TempDir()`: two opens contend (BSD flock is per open file description), and the second error carries `os.Getpid()`.
  - After release, the lock can be re-acquired.
  - The fd has `FD_CLOEXEC` set, so the tmux server can never inherit the lock.
  - With the seam returning `LockHeldError`, `defaultSupervisedExec` returns it, the fake tmux exec records zero calls, and no socket is created.

**A3. Stable self path (item 2)**
- `managed.SelfExecutable(st, exe func() (string, error)) func() (string, error)`. Managed: returns `SelfPath` with no `EvalSymlinks`. Unmanaged: returns `exe()`.
- New field `RunSupervisedOptions.Executable`; `cli/service.go:runService` fills it. The daemon then threads one closure into:
  - `leomcp.ResolveServer` (process.go:952). This also covers `leo-mcp.json` and the `migrate.go` rewrites, since both use `Server.Bin`.
  - `resolveViewerMenuBindings` (the prefix+L binding). Drop the `serviceExecutable` global from it.
  - `wireBridge` (bridge.go:512, sets `LEO_BRIDGE_BIN`): new parameter.
  - `attentionReportCmd` (attention_launch.go:17): becomes a Supervisor field.
  - New `web.Options.LeoBin`, used for `interactiveLeoPath` (consult_runtime.go:82).
- Codex (hooks.go:17):
  - Change `harness.PrepareInteractive(env, cwd)` to `(env, cwd, leoBin)`.
  - Callers: `interactive_runtime.go:223` passes `r.leoPath`; `codex/attention.go:10` and `claude/hooks.go:91` pass `reportCmd[0]`.
  - Delete `defaultLeoHookCommand`'s `os.Executable` and keep the `"leo dispatch report"` fallback for an empty `leoBin`. Attention and interactive hooks must stay byte-identical because codex trusts hooks by hash.
- CLI:
  - `cli/run.go:40,81` gets `ResolveServer(selfExecutable(cfg), …)`.
  - `cli/helpers.go:leoExecutablePath` and `setup/daemon.go:23` (plist) become marker-aware. C1 makes this unreachable in managed mode, but it keeps the spec literal.
- Failing tests, each with an unmanaged fallback row:
  - `EnsureConfig` JSON `command` equals `self_path`.
  - The bind-key argv contains `shellQuoteLiteral(self_path)`.
  - The bridge launcher's `LeoBin` equals `self_path`.
  - `attentionLaunchArgs` passes `[self_path dispatch report]`.
  - Codex `hooks.json` command and the `config.toml` trust entry for that exact string. An old brew-path leo hook is stripped (`withoutLeoHandlers` matches by suffix), so it is not duplicated.
  - `TurnHooks` argv[0] equals `self_path`.

**A4. Self-update defers (item 5)**
- At the top of the `cli/update.go` RunE, before `--pr`, `--unstable`, `--version`, `--check` or `CheckLatestVersion`: if managed, return the exact spec error.
- Beyond spec, recommended: add `update.PackageManagerLeoApp` for any resolved executable under `*.app/Contents/` (pattern `\.app/Contents/.+`; the app ships leo at `Contents/Helpers/leo`, tmux at `Contents/Helpers/tmux`). `DownloadAndReplace*` (update.go:241, prerelease.go:300) refuses on it, even if the marker is invalid. Replacing a signed nested binary would break the app's code signature.
- Test: marker present gives the exact message, a non-nil error, and zero download-seam calls. A bundle path with no marker is refused too.

**A5. e2e (`e2e/managed_test.go`)**
- Setup in a temp home:
  - The managed root has no spaces (`~/.leo/app` in the product); the test uses a root with a space deliberately, to catch quoting regressions.
  - `bin/leo` is a symlink to the built `leoBin`.
  - `tmux/<ver>/tmux` is a symlink to a tmux, where `<ver>` comes from `tmux -V` (the e2e uses its isolated tmux wrapper).
  - The marker points at both.
- Assertions:
  - `state/leo-mcp.json` command equals `self_path`.
  - `tmux -L leo list-keys -T prefix L` contains `self_path`.
  - A second `leo service --supervised` against the same home exits non-zero with the "another leo daemon owns" message.
  - The first daemon's socket still answers.
- The spec's "plist" assertion is replaced by the prefix+L binding, because install refuses in managed mode (C1).

**Verify:** `go test -race ./internal/managed/ ./internal/service/ ./internal/leomcp/ ./internal/harness/... ./internal/web/ ./internal/update/`, then `env -u TMUX go test -race ./internal/cli/`, then `make lint`, then `env -i HOME=$HOME PATH=$PATH USER=$USER TMUX_TMPDIR=$(mktemp -d) make e2e E2E_RUN=TestManaged`.

## Slice B: tmux resolution and the server version guard (items 3, 4)

**B1. `tmux.Resolve(ResolveOptions) Resolution`** (in `internal/tmux`, which does not import `managed`)
- Options: `{ManagedPath string; ServerVersion func(bin) (string, bool); ReadDir; Exists; Locate func() (string, error)}`.
- Result: `{Client, Start, Pending string; Err error}`.
- Logic:
  - Unmanaged: `Locate()` for both `Client` and `Start`.
  - Managed: W = `basename(dirname(ManagedPath))`, the pinned version.
  - Probe the server version V with `ManagedPath`. If that fails, try each `<root>/*/tmux` in sorted order. The first that answers gives V.
  - No server answers: `Client = Start = ManagedPath`.
  - V == W: same as above.
  - V ≠ W and `<root>/V/tmux` exists: `Client = <root>/V/tmux`, `Start = ManagedPath`, and `Pending` = "tmux upgrade pending…".
  - V ≠ W and no copy exists: `Client = ManagedPath` and `Err` is set.
  - Never `kill-server`.
- The real probe's argv is exactly `[bin -L leo display-message -p #{version}]`. Formats expand server-side, so this returns the server's version.
- Failing test: table covering each branch above, plus the exact probe argv.

**B2. Daemon wiring**
- `defaultSupervisedExec` replaces `findTmux()` (process.go:1802) with `Resolve`. `Client` feeds `supervisor.tmuxPath`, `EnsureForegroundServer` and `SuperviseForegroundServer`.
- When no server exists, `Client == ManagedPath`, so `tmux_path` is what starts a new server (see Q1).
- Log `Pending`/`Err` once at startup.
- Replace the remaining lookups that bypass `Locate`:
  - `web.findTmuxPath()` (handlers.go:489, about 15 call sites) becomes `s.tmuxPath()`, fed from a new `web.Options.TmuxPath`.
  - `daemon/server.go:847 tmuxPath()`.
  - `agent/manager.go:1913` LookPath.
  - `peerinbox.go:59`, which currently runs a bare `"tmux"`.
- Test: one handler (`caller.go` show-options) and the injector receive the injected path as argv[0].

**B3. CLI wiring**
- New `cli.resolveTmux()` loads cfg and the marker (and falls back to `Locate` if config load fails), then calls `Resolve`.
- It replaces these seams: `tmuxLocate` (tmux.go:53, used for attach and attach_start), `viewerLocateTmux`, reparent `locate`, and prereq's `locateTmux` as used by doctor and validate.
- The CLI stays silent on `Pending` (see Q10).
- Test: local `leo agent attach` with V ≠ W and a copy present → `agentSyscallExec` gets path `<root>/V/tmux` and the full argv.

**Verify:** `go test -race ./internal/tmux/ ./internal/web/ ./internal/service/ ./internal/agent/`, then `env -u TMUX go test -race ./internal/cli/`, then the e2e command above.

## Slice C: service routing, handover, doctor, pairing (items 6, 8, 9)

**C1. The manager owns the service (item 6)**
- The managed plist's argv is `[Helpers/leo, service, --supervised]` with no `--config`; the daemon resolves `~/.leo` itself and logs to `state/service.log`.
- New in `daemon_darwin.go`:
  - `RestartLabel(label)` runs `kickstart -k gui/<uid>/<label>`. On failure it does not consult `plistPath`.
  - `DaemonStatusLabel(label)`.
  - `bootstrapWithRetry(path, sleep)`, extracted from `InstallDaemon` at :186.
- `daemon_restart.go:32` `restart` becomes a marker-aware closure. This covers `service restart`, the web restart button (which shells out to the CLI) and `web enable`.
- Refusals use one const message naming Leo.app and the label. They apply to:
  - `service start` and `service start --daemon`.
  - `service stop` and `service stop --daemon`.
  - The setup wizard's daemon install (`setup/daemon.go`), which becomes a skip with an info line.
- The spec's install/uninstall commands are really `start --daemon` and `stop --daemon`; no separate `install`/`uninstall` subcommands exist.
- `leo status` (status.go:134) reports the managed label.
- **JSON rule** (every `--json` command in this plan: `service restart`, `handover`, `pair`, `web enable`): stdout is only the JSON object; logs go to stderr; a failure is `{error:<stable code>, message?}` with a non-zero exit.
- `leo service restart --json`: down but job loaded gives exit 0 and `{ok:true, drained:false, restarted:true}`; job not loaded gives exit 1 and `not_registered`; a drain timeout without `--force` gives exit 1 and `drain_timeout`.
- Tests:
  - Managed restart argv is exactly `[[launchctl kickstart -k gui/<uid>/studio.blackpaw.leo.daemon]]`, and no recorded argv contains `com.blackpaw.leo`.
  - Each refused command records zero `runCommand`/`startProcess` calls.

**C2. `leo service handover` (item 8)**
- Files: new `cli/service_handover.go`, `service/handover_darwin.go` and `_other.go` (which returns `{ok:false, step:"preflight", error:"unsupported"}`).
- Result: `{ok, step, error, previous_label, env, plist_backup, rolled_back, managed_stopped}` (failures follow the JSON rule in C1: stable `error` code, non-zero exit).
- In `--json` mode stdout carries exactly one JSON object, drain progress goes to stderr, and `ok:false` exits non-zero.
- Drain: reuse `drainWatch` with `restart` set to the critical section below. Mode is non-interactive `graceful:true`; `--drain-timeout`/`--force` are as in restart.
- `previous_label` is `daemonLabel(home)`, not hard-coded. That keeps the live test on an isolated home's hashed label.

`--to-app --label L`:
1. Preflight: no marker, L valid and ≠ `previous_label`, the plist exists. A running daemon with no launchd job fails with `error=no_launchd_job` (see Q3). If the plist exists but the job is **not loaded** (no daemon running), skip steps 3 and 4 (drain, bootout) and only move the plist aside (step 5).
2. Capture env with `plutil -extract EnvironmentVariables json -o - <plist>`. A missing key means `{}`.
3. Drain, then commit.
4. `launchctl bootout gui/<uid>/<prev>`. On failure: cancel the drain.
5. Rename the plist to `<plist>.handed-over`. On failure: `bootstrap` the plist again and cancel the drain.
6. Wait (bounded) until `daemon.lock` can be acquired. On failure: rename back and bootstrap.
- If a rollback step itself fails, the result reports `rolled_back:false` (with the failing `step`); a successful rollback reports `rolled_back:true`.

`--to-cli`:
1. Preflight: marker valid, backup exists, and the backup's `ProgramArguments.0` exists and is executable (`plutil -extract ProgramArguments.0 raw`). Q7 covers downgrades.
2. Drain and commit through the managed daemon.
3. `bootout gui/<uid>/<marker label>` (see Q2).
4. Wait for the lock.
5. **Rename `managed.json` to `managed.json.releasing`.** Without this, the CLI daemon starts while the marker is still live and persists `self_path` everywhere. This deviates from the spec's ordering in spirit only; the marker is still deleted only after the CLI daemon is up.
6. Rename the backup to the plist, then `bootstrapWithRetry`, then `WaitReady`, then delete `.releasing`.
7. Rollback: bootout the CLI label, restore the backup, rename `.releasing` back, and set `managed_stopped:true` so the app re-registers its LaunchAgent.

Optional `--dry-run` (beyond spec, recommended): prints the planned argv without running it. It makes the live test safe, given the 2026-07-08 production-bounce incident.

Failing tests:
- The happy-path argv sequence for each direction.
- One table row per failing step, asserting the rollback argv, the `step` value, and the drain `Cancel` call.

**C3. `leo doctor`** (after B1)
- Report: managed yes/no, manager, label and its `launchctl print` status, `self_path` and its readlink target, `tmux_path`, the `Resolution` (Client/Start/Pending/Err), and whether `tmux_path -V` matches the dir name.
- Show any marker warning.
- Warn if the `com.blackpaw.leo` plist is still loaded while managed: the lock loser respawns every 10s.
- Test: golden lines with fakes.

**C4. Pairing (item 9)**
- Extract `buildPairing(cfg, name, ifaceAddrs) (Payload, warnings []string, expires time.Time, error)` from `runPair` (devices.go:47). Text mode renders as today.
- `--json` prints the payload fields plus `warnings` and `expires_at` (see Q5). With the web listener disabled, stdout is `{"error":"web_disabled"}` and the exit is non-zero, with usage silenced.
- `leo web enable [--bind] [--json]`:
  - Sets `web.enabled` (and bind), validates, then `config.Save`. This drops comments in leo.yaml, which matches what other CLI writers already do.
  - If the daemon is running, it runs `gracefulRestart{graceful, force}` through the C1 routing.
  - Output: `{ok, changed, restarted, web:{enabled,bind,port}}`.
  - No change means no write and no restart.
- Tests:
  - JSON golden output and the exact `web_disabled` stdout.
  - Config file contents after the write.
  - The restart seam is called once with the managed-label argv.
  - The no-op case makes zero restart calls.
  - An invalid bind leaves the file unchanged.

**Verify:** unit tests as above, then `make test`, then live per spec. Use an isolated `LEO_HOME` with a hand-registered LaunchAgent under its hashed label. Run `--dry-run` first and confirm `previous_label ≠ com.blackpaw.leo` before any real run. **Never touch production without Evan's go.**

## Slice D: release artifact and docs (items 10, 11)

**D1. `.goreleaser.yaml`** (config, so no TDD)
- Build gets `id: leo`.
- `universal_binaries: [{id: leo-universal, ids: [leo], replace: false, name_template: leo}]`.
- `notarize.macos.ids: [leo, leo-universal]`. The default is the project name only, so the universal build must be listed explicitly.
- Archives: the existing one becomes `id: default, ids: [leo]`. Add `ids: [leo-universal]` with `name_template: "{{ .ProjectName }}_{{ .Version }}_darwin_universal"`, since `.Arch` is `all` for universal binaries.
- `homebrew_casks.ids: [default]`.
- The per-arch archives that `leo update` (update.go:252) depends on stay unchanged.
- Verify:
  - `goreleaser check`.
  - `goreleaser release --snapshot --clean --skip=publish,sign,notarize`.
  - The universal tarball and all 4 per-arch tarballs exist.
  - `lipo -info` on the extracted universal binary shows x86_64 arm64.
  - `checksums.txt` lists the universal tarball.
  - The generated `dist/homebrew/Casks/leo.rb` still uses per-arch URLs.

**D2. `release.yml`**
- After goreleaser: read the sha from `dist/checksums.txt`, then run `gh api repos/blackpaw-studio/leoterm/dispatches -f event_type=leo-release -f 'client_payload[version]=<v-less>' -f 'client_payload[sha256]=…'` with `GH_TOKEN=secrets.LEOTERM_DISPATCH_TOKEN`.
- If the secret is missing, emit a `::warning::` and skip.
- **Needs Evan:** a fine-grained PAT with leoterm Contents read/write, stored in 1Password Olympus and as a repo secret.
- Verify: `actionlint`, plus a local run of the step script against a fixture `checksums.txt`.

**D3. Docs**
- New `docs/configuration/managed-install.md`: marker schema and validation, a table of behaviour by command, handover commands and JSON shapes, the tmux guard, the lock, and a "repair" section.
- Add it to the `mkdocs.yml` nav.
- Update `docs/cli/service.md`, `update.md`, `web.md` and the pairing docs.
- Verify: `make docs`.

## Risks
1. **`launchctl bootout` might kill the tmux server.** Only survival of `kickstart -k` is proven. Check this live on an isolated daemon before building C2. If bootout kills it, agents are lost and the design needs re-specifying.
   **Gate passed (2026-10-20, live).** Isolated daemon: label `com.blackpaw.leo.gatetest`, plist `EnvironmentVariables.TMUX_TMPDIR` set to a temp dir (socket `<tmp>/tmuxtmp/tmux-501/leo`, distinct from production's `/private/tmp/tmux-501/leo`), `bin/leo service --supervised --config <tmp>/leo.yaml`, web disabled. The daemon (pid 2452) started its foreground `-L leo` server (pid 2460); a dummy session was created. After `launchctl bootout gui/501/com.blackpaw.leo.gatetest` (rc 0, job gone) the server was still pid 2460 and the dummy session and its pane (pid 2529) were alive. Cleanup killed only the test server; production socket and sessions untouched.
2. **Local Network attribution after takeover.** The adopted server's creator process is gone, so the grant may lapse. Doctor detects it and `leo service reparent` fixes it (that bounces agents). Whether to offer reparent is an app-side UX call.
3. **Lock loser crash-loop.** Under `KeepAlive true`, a daemon that loses the lock respawns about every 10s while both jobs are registered. It's noise, not harm; C3 surfaces it.
4. **Universal notarization can't be tested locally.** It only runs on a real tag. Follow the release guard: CI green in its own call before tagging; pulling a release means reverting the tap cask commit.
5. **Agents launched before takeover keep the brew path** until they relaunch. `EnsureConfig` and migrate rewrite it on their next launch.

## Open questions (spec doesn't settle these)
1. **tmux crash-restart while an upgrade is pending:** I recommend restarting with `Client` (V). Every path cached as a string stays valid. Otherwise a mutable resolver has to be threaded through about 10 service call sites.
2. **`--to-cli` ordering:** I recommend leo boots out the marker's label, then the app calls `SMAppService.unregister()`. If the app unregisters first, the drain becomes impossible.
3. **Non-launchd daemon** (`leo service start` with a pid file): I recommend preflight failure `no_launchd_job`.
4. **Remote attach to a managed server Mac:** the SSH paths (tmux.go:254,344,443) run `host.tmux_path`, default `tmux`, which won't be the bundled tmux. Options are to have the remote side invoke `leo`, or to document setting `tmux_path`.
5. **`pair --json` shape:** I recommend flat fields plus `expires_at` plus `qr_payload` (the exact string to encode). This contract needs agreement with leoterm.
6. **`web enable --bind` with a non-loopback address:** without `web.allowed_hosts` set, the daemon refuses requests to the LAN URLs and the phone can't connect. I suggest a repeatable `--allow-host` flag.
7. **Release to CLI can downgrade.** The restored brew binary may be older than the state on disk. Options are to warn or refuse when `<old> --version` is lower.
8. **`repository_dispatch` scope:** I recommend skipping tags that contain `-`, and sending the version without the `v`.
9. **`leo update --check` when managed:** I recommend refusing it too.
10. **"tmux upgrade pending" output:** I recommend the daemon log and doctor only; the CLI stays silent.
11. **Bundle-path update refusal (A4):** it goes beyond the spec. I recommend it.
12. **Marker on Linux:** I recommend ignoring it with a warning.

Sources: [GoReleaser universal binaries](https://goreleaser.com/customization/universalbinaries/), [GoReleaser notarize](https://goreleaser.com/customization/notarize)

## Orchestrator decisions (2026-10-20)
Spec is the source of truth where these conflict.
- Q1: accept: crash-restart uses `Client` (V).
- Q2: accept: leo boots out the marker label; the app unregisters afterwards (sent to leoterm).
- Q3: accept: `no_launchd_job` preflight failure.
- Q4: out of scope. Document that remote attach to a managed Mac needs `tmux_path` set; follow-up issue.
- Q5: accept flat fields + `expires_at` + `qr_payload`; leoterm to confirm before C4 merges.
- Q6: accept repeatable `--allow-host` on `leo web enable`.
- Q7: `--to-cli` warns (JSON `warnings[]`) when the restored binary is older; does not refuse.
- Q8, Q9, Q10, Q11, Q12: accept recommendations.
- Risk 1 is a gate: C2 starts with a live isolated-daemon check that `launchctl bootout` leaves the `-L leo` tmux server alive. If it doesn't, stop and escalate.
- D2: workflow ships with the skip-if-missing secret; Evan creates the PAT separately.
