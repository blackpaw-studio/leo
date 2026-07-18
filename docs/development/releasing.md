# Releasing

Leo uses [GoReleaser](https://goreleaser.com/) and GitHub Actions to automate releases. Pushing a version tag triggers the full pipeline: CI validation, cross-platform builds, and GitHub Release creation.

## How It Works

```
git tag v0.2.0 ──> GitHub Actions ──> CI (test + lint)
                                        │
                                        ▼
                                    GoReleaser
                                    ├── Build binaries (darwin/linux × amd64/arm64)
                                    ├── Sign + notarize darwin binaries (Apple)
                                    ├── Create GitHub Release with changelog
                                    └── Upload archives + checksums
```

The release workflow (`.github/workflows/release.yml`) calls the CI workflow as a prerequisite — no release is published unless tests and lint pass.

## Creating a Release

### 1. Ensure main is ready

```bash
git checkout main
git pull
make test
make lint
```

### 2. Tag and push

Use the `make tag` shortcut:

```bash
make tag V=0.3.3
```

This creates an annotated tag `v0.3.3` and pushes it to origin. Equivalent to:

```bash
git tag -a v0.3.3 -m "Release v0.3.3"
git push origin v0.3.3
```

### 3. Monitor the release

The [Release workflow](https://github.com/blackpaw-studio/leo/actions/workflows/release.yml) runs automatically. When it completes, the [Releases page](https://github.com/blackpaw-studio/leo/releases) will have:

- Release notes auto-generated from commit history
- Archives for each platform: `leo_0.3.3_darwin_amd64.tar.gz`, etc.
- A `checksums.txt` file

## Versioning

Leo follows [Semantic Versioning](https://semver.org/):

| Tag | When |
|-----|------|
| `v0.1.0` | Initial release |
| `v0.2.0` | New features, backward compatible |
| `v0.2.1` | Bug fixes only |
| `v1.0.0` | Stable API, breaking changes from 0.x |
| `v1.1.0-beta.1` | Pre-release |

Tags must use the `v` prefix (e.g., `v1.2.3`, not `1.2.3`).

## Changelog

The changelog is generated automatically from commit messages between tags. Commits prefixed with `docs:`, `test:`, or `chore:` are excluded. Use [conventional commits](https://www.conventionalcommits.org/) for clean release notes:

```
feat: add background daemon mode
fix: resolve config resolution on nested paths
refactor: simplify cron marker parsing
```

To edit release notes after publishing, use the GitHub Releases web UI.

## Install Script

The repo includes an `install.sh` that users can curl-pipe to install the latest release:

```bash
curl -fsSL https://raw.githubusercontent.com/blackpaw-studio/leo/refs/heads/main/install.sh | sh
```

The script auto-detects OS and architecture, downloads the matching archive from the GitHub Release, and installs the binary to `~/.local/bin` (or `INSTALL_DIR`).

## Testing Locally

To verify the GoReleaser configuration without publishing:

```bash
make snapshot
```

This creates a full build in `dist/` without pushing anything.

## macOS Signing & Notarization

The darwin binaries are codesigned with a Developer ID Application identity
(hardened runtime + timestamp) and notarized by Apple during the release, via
GoReleaser's built-in [quill](https://github.com/anchore/quill) support — it
runs cross-platform on the ubuntu runner, no macOS machine involved.

The `notarize` block in `.goreleaser.yaml` is gated on `MACOS_SIGN_P12` being
set, so local snapshots and the prerelease/unstable workflows build unsigned
(ad-hoc) binaries without needing any credentials.

Plain Mach-O binaries cannot have a notarization ticket stapled (only apps,
dmgs, and pkgs can), so Gatekeeper verifies notarization with an online check
on first launch of a quarantined copy.

## Setup (Maintainers)

`GITHUB_TOKEN` is provided automatically by GitHub Actions. The remaining
secrets are configured on the repo:

| Secret | Contents |
|--------|----------|
| `HOMEBREW_TAP_GITHUB_TOKEN` | Token with push access to `blackpaw-studio/homebrew-tap` |
| `MACOS_SIGN_P12` | Base64-encoded Developer ID Application identity (cert + key) |
| `MACOS_SIGN_PASSWORD` | Passphrase for the p12 |
| `MACOS_NOTARY_ISSUER_ID` | App Store Connect API issuer UUID |
| `MACOS_NOTARY_KEY_ID` | App Store Connect API key ID |
| `MACOS_NOTARY_KEY` | Base64-encoded App Store Connect API key (`.p8`) |

The signing identity and API key originals live in 1Password (Blackpaw Studio
vault). The Developer ID certificate is effectively irreplaceable — Apple
allows neither self-service revocation nor new Developer ID certs without
Account Holder interaction, and the account is at its certificate cap — so
treat the 1Password item as the source of truth and never delete it.

## Troubleshooting

**CI fails on tag push**
:   Fix the issue on `main`, delete the tag, and re-tag:

    ```bash
    git tag -d v0.3.3
    git push origin :refs/tags/v0.3.3
    # fix and push to main
    make tag V=0.3.3
    ```

**GoReleaser config errors**
:   Validate locally before tagging:

    ```bash
    goreleaser check
    make snapshot
    ```
