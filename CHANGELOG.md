# Changelog

All notable user-visible changes to Leo are documented here.

## [Unreleased]

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
