# Changelog

All notable user-visible changes to Leo are documented here.

## [Unreleased]

### Security

- **Env values no longer travel to agents through listings.** `leo_list_agents`
  returned each agent's full env map verbatim, and `leo_list_templates` dumped
  every template's config wholesale, so a routine listing call deposited live
  credentials (1Password service-account tokens and friends) into the calling
  agent's context and transcript. `GET /task/list` had the same problem. Agent
  records now carry no env at all; template and task listings carry `env_keys`
  (key names only). `leo template show` and `leo run --dry-run` mask
  credential-looking values.
- **Agent env is no longer part of the tmux start command.** It travels as
  `new-session -e KEY=VALUE` instead of shell exports, so credentials stop
  being readable for the life of a session via
  `tmux list-panes -F '#{pane_start_command}'`.
- **Agents get their own, less privileged token.** `LEO_API_TOKEN` was the
  operator's `api.token`, which `/login` accepts and which reached the config
  editor (where template env renders in full). Agents and scheduled tasks now
  get `agent.token`, accepted only on `/api/*` and the agent-messaging routes.

  None of this isolates agents from each other — they run as your user and can
  read `leo.yaml` directly. It removes the incidental copies and bounds what a
  leaked token is worth. Consider rotating any credential that has been in
  agent env.

### Changed

- **BREAKING: the codex harness option `sandbox` is now `permission_mode`.**
  Configs carrying `harness_options.sandbox` under a codex-harness scope fail
  validation with an explicit "renamed to" error — and because validation runs
  on every config load, that takes down the CLI *and* the daemon until the key
  is renamed. There is no automatic migration. Rename it before upgrading:

  ```yaml
  templates:
    codex:
      harness: codex
      harness_options:
        sandbox: workspace-write        # old
        permission_mode: workspace-write  # new
  ```

  The three existing values (`read-only`, `workspace-write`,
  `danger-full-access`) are unchanged and still passed as `--sandbox`. The key
  was renamed because the new fourth value is not a sandbox setting — see
  Added, below.
- **tmux 3.2+ is now required** (it added `-e` on `new-session`). `leo setup`
  refuses to complete and `leo validate` reports an error on older versions,
  instead of letting every agent spawn fail silently in the restart loop.
  Spawn failures now also surface tmux's own stderr rather than a bare
  `exit status 1`.
- A `PATH` key in a task's or template's `env:` is now dropped with a warning.
  It never took effect — leo exports the daemon's PATH into the session after
  it — so this only makes the existing behaviour visible.
- `GET /api/template/list` returns a sorted array of trimmed records rather
  than the raw templates map, and `GET /task/list` returns a trimmed
  projection. Neither includes env values.

### Added

- **`permission_mode: approve-for-me` for the codex harness.** Codex's
  `--approve-for-me` preset: it implies the `workspace-write` sandbox, sets
  approval policy `on-request`, and routes each escalation to codex's
  *automatic* approval reviewer rather than to a human — so leo drops its usual
  `-a never` pinning for this value only. It stays safe unattended: a denied
  escalation comes back to the model as a developer message and the turn ends
  with the agent asking in-band, rather than raising a blocking TUI modal that
  could strand the readiness probe. The reviewer is a model making a judgement
  call, so it is a weaker boundary than a sandbox — prefer `workspace-write`
  unless an agent genuinely needs to escalate.
- **PR prerelease builds.** Every PR from this repo now produces a
  cosign-signed installable binary as a workflow artifact. Install with
  `leo update --pr <n>` for the latest passing run on a PR, or
  `leo update --version pr-<n>-<sha>` to pin to a specific build. A
  sticky PR comment shows both forms plus a `gh run download` fallback.
  Artifacts are retained for 14 days; releases page is untouched.

### Fixed

- **Unknown web/API routes now return 404 instead of a misleading 405.** The
  dashboard redirect was registered as `GET /`, which under Go's ServeMux is a
  *prefix* pattern matching every path — so a POST to a route that did not
  exist came back as `405 Method Not Allowed, Allow: GET`, reading as "right
  path, wrong method" and sending at least one API client's debugging in the
  wrong direction for days. Wrong-method calls on routes that *do* exist were
  meanwhile flattened into a bogus 404. Both now report accurately, with a
  correct `Allow` header on real 405s. Unauthenticated callers still cannot
  tell an unknown path from a real one.

## [0.4.1] — 2026-04-20

### Removed

- Unused `prereq.FindExistingWorkspaces` helper and its tests. The
  function stat'd every subdirectory of `$HOME` looking for `leo.yaml`,
  which would have triggered macOS TCC prompts for `~/Documents`,
  `~/Downloads`, `~/Music`, and `~/Photos` if it had ever been wired up.

## [0.4.0] — 2026-04-20

### Added

- **tmux: dedicated `-L leo` socket, `display-popup` overlay when attaching
  from inside tmux, `--cc` control-mode flag for iTerm2/WezTerm, and
  interactive session picker.** (#64)
- `CODE_OF_CONDUCT.md` and pre-release docs / release-hygiene polish. (#69)

### Changed

- **Pre-release security hardening across install, daemon, web, and MCP**:
  `install.sh` verifies cosign signatures opportunistically when `cosign`
  is available, web and daemon auth paths tightened, MCP surface locked
  down. (#67)
- Go correctness pass from pre-release review: `sync/atomic.Bool` for
  restart state, `context.Context` threaded through `daemon.Send` and the
  validation pipeline, `errors.Is(err, http.ErrServerClosed)` for clean
  shutdown detection, and propagated `os.Remove` errors instead of silent
  failures. (#68)

### Fixed

- **MCP client now sends `Authorization: Bearer` header to the daemon;
  `leo service` injects `LEO_API_TOKEN` into supervised processes.** (#65)

## [0.3.2] — 2026-04-18

### Added
- Process cards in the web UI now have a **Restart** button next to Interrupt, which kills the tmux session so the supervisor auto-restarts the process.

### Fixed
- `leo setup` no longer drops the top-level `web`, `client`, and `templates` sections when re-run against an existing config. `buildConfig` now preserves all config sections alongside `defaults`, `processes`, and `tasks`.

## [0.3.1] — 2026-04-18

### Fixed
- `leo update` no longer fails with `parsing certificate: no PEM block found` against releases whose `checksums.txt.pem` is base64-wrapped (as emitted by GoReleaser v2). `parseLeafCertificate` now transparently base64-decodes the artifact when it has no PEM header.

## [0.3.0] — 2026-04-18

### Changed

- **`prompt` package gained `PromptNonEmpty`**, a retry helper that
  returns `io.EOF` instead of looping forever when stdin closes. The
  client-setup wizard uses it for required host fields so piped or
  exhausted input aborts cleanly instead of hanging the process.
- **`buildClientConfig` is now a pure function.** The "replace default
  host?" prompt moved to `runClientSetup` via a new `resolveDefaultHost`
  helper, and the builder takes the resolved default as a plain string —
  making the builder trivially testable and the call graph obvious.
- **Fresh client installs no longer emit empty `processes: {}` /
  `tasks: {}` keys in `leo.yaml`.** Nil maps stay nil so the generated
  file is truly minimal.
- `promptSetupMode` returns `bool` (`isClient`) instead of
  `"client"`/`"server"`. Internal refactor only.

### Added

- **`leo setup` now supports client-mode installs.** The wizard asks
  whether Leo will run on this machine (server) or drive a remote host
  over SSH (client). The client path collects a nickname, SSH target,
  optional port, and optional remote `leo` binary path, optionally tests
  SSH connectivity, and writes a `client:` section to `~/.leo/leo.yaml`
  — no workspace, `USER.md`, `CLAUDE.md`, skills, or daemon install.
  Re-running setup on a client auto-detects client mode from the
  existing config.

### Fixed

- **Client setup no longer aliases map state back into the loaded config.**
  `buildClientConfig` now deep-copies `Processes`, `Tasks`, `Templates`,
  and `Client.Hosts` via `maps.Clone` before mutating, so a re-entered
  setup session cannot silently modify the in-memory config.
- **Client-mode detection aligned with `Config.IsClientOnly()`**; a config
  with hosts plus tasks or templates (but no processes) no longer defaults
  the setup prompt to client.
- **SSH connectivity test now runs with `BatchMode=yes` and
  `ConnectTimeout=8`** so the probe fails fast instead of blocking on
  host-key confirmation, password, or 2FA prompts.
- **Non-numeric `-p` port values in an existing client config are warned
  about and ignored** instead of being silently coerced to 0.

## [0.2.2] — 2026-04-18

### Fixed

- **`leo setup` re-prompted for user profile when `USER.md` used custom
  headers.** The setup wizard parsed existing `USER.md` files by matching
  exact template headers (`## Name`, `## Role`, …); files with any other
  structure parsed as empty, so setup silently re-prompted for every field
  and then overwrote the existing file on save. Setup now detects `USER.md`
  by file existence and preserves custom-format files when the user
  declines to update.

## [0.2.1] — 2026-04-18

### Fixed

- **Daemon state drift on macOS.** `leo status`, `leo service remove`, and
  `leo service restart` now treat `launchctl` as the source of truth for
  daemon state rather than the plist file on disk. This fixes contradictory
  output when a launchd service was still registered after its plist had
  been removed (previously `leo status` would report "Service: running"
  alongside "Daemon: not installed"). (#61)

## [0.2.0] — 2026-04-18

### Added

- **Web UI authentication.** The browser UI is now gated by a session cookie
  (login form) and `/api/*` accepts a bearer token. Binds remain loopback by
  default; to expose the UI on a LAN address set `web.bind` and populate
  `web.allowed_hosts`. Host/Origin pinning blocks DNS-rebinding and
  cross-origin POSTs. (#46, #60)
- **Cosign-verified updates.** `leo update` now verifies the keyless Sigstore
  signature on `checksums.txt` before trusting it, pinning the issuing
  identity to the release workflow's GitHub OIDC token. (#47)
- **Supervisor crash-loop diagnostics.** When a supervised process exits
  abnormally, Leo captures the exit signal and the tail of stderr and
  surfaces it in `leo status` and logs, instead of silently restarting.
  Backoff now resets after 10 minutes of healthy uptime. (#59)
- **Size-based log rotation** for `service.log` via lumberjack — no more
  unbounded growth. (#58)
- **CLI UX overhaul.** `--version` flag, richer `Long` and `Example`
  sections on every command, `--json` output on `process`, `task`,
  `template`, `session`, `config`, `validate`, `run`, and `status`,
  confirm-on-remove for destructive commands, flag-first `task add`,
  non-TTY safety for `agent`, and shell completion. (#50–#55)
- **Homebrew formula auto-publish** on release (blackpaw-studio/homebrew-tap). (#41)
- **Homebrew-aware `leo update`** — detects brew installs and delegates to
  `brew upgrade` instead of overwriting the brew-managed binary. Service
  workspaces also auto-sync on start. (#43)
- **Agent spawn collision prompt** when a new agent's workspace would clash
  with an existing owner/repo checkout. (#48)
- **Recommended tmux config** documentation and an Example Usage guide. (#42, #56)

### Fixed

- Supervisor no longer double-prefixes tmux session names with `leo-`. (#57)
- Supervisor validates env keys and the web port before shell interpolation,
  so malformed config can't inject into the launch command. (#44)

### Security

- `add_dirs` paths are validated, gosec is SHA-pinned in CI, and the
  `install.sh` bootstrap script ships with a published SHA-256 checksum
  as a release asset. (#45)
- All third-party GitHub Actions in the release workflow are pinned by
  commit SHA.

### Docs

- README revamped for scannability; CLI reference synced with the UX
  overhaul; stale "no built-in auth" warnings removed now that web auth
  ships by default.
- The `go install` path is now `github.com/blackpaw-studio/leo/cmd/leo@latest`
  (the previous path pointed at the repo root, which has no `main` package).

## [0.1.0] — 2026-04-16

Initial public release.

[Unreleased]: https://github.com/blackpaw-studio/leo/compare/v0.4.1...HEAD
[0.4.1]: https://github.com/blackpaw-studio/leo/compare/v0.4.0...v0.4.1
[0.4.0]: https://github.com/blackpaw-studio/leo/compare/v0.3.2...v0.4.0
[0.3.2]: https://github.com/blackpaw-studio/leo/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/blackpaw-studio/leo/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/blackpaw-studio/leo/compare/v0.2.2...v0.3.0
[0.2.2]: https://github.com/blackpaw-studio/leo/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/blackpaw-studio/leo/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/blackpaw-studio/leo/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/blackpaw-studio/leo/releases/tag/v0.1.0
