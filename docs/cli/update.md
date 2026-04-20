# leo update

Download the latest Leo release and replace the running binary.

## Usage

```bash
leo update [--check]
```

## Description

Fetches the latest release from GitHub, verifies its cosign signature (keyless, via the public Sigstore transparency log), and swaps it in atomically. If Leo was installed via Homebrew, `leo update` delegates to `brew upgrade leo` so your package manager stays in sync.

Workspace templates (`CLAUDE.md`, `skills/*.md`) re-sync automatically whenever the service starts — **restart the daemon after updating** to pick up any template changes.

## Flags

| Flag | Description |
|------|-------------|
| `--check` | Report whether an update is available without installing. |

An `--allow-unsigned` escape hatch exists for releases published without a cosign signature (SHA-256 checksum only). It is hidden from `--help` and should only be used when explicitly advised; the same behavior can be toggled with the equivalent env var.

## Examples

```bash
# Install the latest release
leo update

# Check for an update without installing
leo update --check
```

## See Also

- [Releasing](../development/releasing.md) — how Leo releases are built and signed
