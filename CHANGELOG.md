# Changelog

All notable user-visible changes to Leo are documented here.

## [Unreleased]

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

[Unreleased]: https://github.com/blackpaw-studio/leo/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/blackpaw-studio/leo/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/blackpaw-studio/leo/releases/tag/v0.1.0
