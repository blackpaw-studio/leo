# leo update

Download the latest Leo release and replace the running binary.

## Usage

```bash
leo update [--check]
leo update --pr <number>
leo update --version pr-<number>-<short-sha>
```

## Description

Fetches the latest release from GitHub, verifies its cosign signature (keyless, via the public Sigstore transparency log), and swaps it in atomically. If Leo was installed via Homebrew, `leo update` delegates to `brew upgrade leo` so your package manager stays in sync.

Workspace templates (`CLAUDE.md`, `skills/*.md`) re-sync automatically whenever the service starts — **restart the daemon after updating** to pick up any template changes.

## Flags

| Flag | Description |
|------|-------------|
| `--check` | Report whether an update is available without installing. |
| `--pr <n>` | Install the most recent successful prerelease build for PR `n`. Needs a GitHub token (see below). |
| `--version <tag>` | Pin to a specific version. Currently supports `pr-<n>-<sha>` for PR builds; stable releases are still installed via the default no-flag form. |

An `--allow-unsigned` escape hatch exists for releases published without a cosign signature (SHA-256 checksum only). It is hidden from `--help` and should only be used when explicitly advised; the same behavior can be toggled with the equivalent env var.

## Installing PR builds

The prerelease workflow uploads a signed `leo-prerelease` workflow artifact for every PR. `leo update --pr <n>` resolves the most recent passing run on that PR, downloads the artifact, verifies its checksum and cosign signature (identity: `prerelease.yml@refs/pull/<n>/merge`), and replaces the running binary.

Authentication: `leo update --pr` tries the following in order, and errors with a helpful message if none works.

1. `$LEO_GITHUB_TOKEN` (leo-specific override)
2. `$GH_TOKEN` (gh CLI standard)
3. `$GITHUB_TOKEN` (Actions / generic)
4. `gh auth token` shell-out if `gh` is on `PATH`

The PR build is *not* a release. The cosign identity is workflow-bound, not tag-bound, and the binary version reports `pr-<n>-<sha>`. Don't ship PR builds to production.

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
```

## See Also

- [Releasing](../development/releasing.md) — how Leo releases are built and signed
