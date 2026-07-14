# leo update

Download the latest Leo release and replace the running binary.

## Usage

```bash
leo update [--check]
leo update --pr <number>
leo update --version pr-<number>-<short-sha>
leo update --unstable
leo update --version main-<short-sha>
```

## Description

Fetches the latest release from GitHub, verifies its cosign signature (keyless, via the public Sigstore transparency log), and swaps it in atomically. If Leo was installed via Homebrew, `leo update` delegates to `brew upgrade leo` so your package manager stays in sync.

Operational instructions/skills are served on demand via the `leo_skill` MCP tool rather than synced files, so they update automatically with the binary — no workspace re-sync or daemon restart required to pick them up.

## Flags

| Flag | Description |
|------|-------------|
| `--check` | Report whether an update is available without installing. |
| `--pr <n>` | Install the most recent successful prerelease build for PR `n`. Needs a GitHub token (see below). |
| `--unstable` | Install the most recent passing build of the `main` branch. Needs a GitHub token (see below). |
| `--version <tag>` | Pin to a specific version. Supports `pr-<n>-<sha>` for PR builds and `main-<sha>` for main builds; stable releases are still installed via the default no-flag form. |

An `--allow-unsigned` escape hatch exists for releases published without a cosign signature (SHA-256 checksum only). It is hidden from `--help` and should only be used when explicitly advised; the same behavior can be toggled with the equivalent env var.

## Installing PR builds

The prerelease workflow uploads a signed `leo-prerelease` workflow artifact for every PR. `leo update --pr <n>` resolves the most recent passing run on that PR, downloads the artifact, verifies its checksum and cosign signature (identity: `prerelease.yml@refs/pull/<n>/merge`), and replaces the running binary.

Authentication: `leo update --pr` tries the following in order, and errors with a helpful message if none works.

1. `$LEO_GITHUB_TOKEN` (leo-specific override)
2. `$GH_TOKEN` (gh CLI standard)
3. `$GITHUB_TOKEN` (Actions / generic)
4. `gh auth token` shell-out if `gh` is on `PATH`

The PR build is *not* a release. The cosign identity is workflow-bound, not tag-bound, and the binary version reports `pr-<n>-<sha>`. Don't ship PR builds to production.

## Installing main (unstable) builds

Every push to `main` triggers the `unstable.yml` workflow, which builds a goreleaser snapshot, cosign-signs it, and uploads a `leo-unstable` workflow artifact (retained 14 days). `leo update --unstable` resolves the most recent passing run on `main`, downloads that artifact, verifies its checksum and cosign signature (identity: `unstable.yml@refs/heads/main`), and replaces the running binary.

Use this when a fix has merged to `main` but hasn't appeared in a tagged release yet.

Authentication: `leo update --unstable` tries the following in order, and errors with a helpful message if none works.

1. `$LEO_GITHUB_TOKEN` (leo-specific override)
2. `$GH_TOKEN` (gh CLI standard)
3. `$GITHUB_TOKEN` (Actions / generic)
4. `gh auth token` shell-out if `gh` is on `PATH`

Installed unstable builds report a `main-<sha>` version string. Running `leo update` (no flags) to a tagged release always supersedes a main build, so `--unstable` is never a dead end.

The unstable build is *not* a release. The cosign identity is workflow-bound to `main`, not tag-bound, and the binary version reports `main-<sha>`. Don't run unstable builds in production.

## Examples

```bash
# Install the latest release
leo update

# Check for an update without installing
leo update --check

# Install the latest prerelease build for PR #42
leo update --pr 42

# Pin to a specific PR build
leo update --version pr-42-a1b2c3d

# Install the newest passing main build
leo update --unstable

# Pin to a specific main build
leo update --version main-a1b2c3d
```

## See Also

- [Releasing](../development/releasing.md) — how Leo releases are built and signed
