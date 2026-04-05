# Releasing

Leo uses [GoReleaser](https://goreleaser.com/) and GitHub Actions to automate releases. Pushing a version tag triggers the full pipeline: CI validation, cross-platform builds, GitHub Release creation, and Homebrew formula updates.

## How It Works

```
git tag v0.2.0 ──> GitHub Actions ──> CI (test + lint)
                                        │
                                        ▼
                                    GoReleaser
                                    ├── Build binaries (darwin/linux × amd64/arm64)
                                    ├── Create GitHub Release with changelog
                                    ├── Upload archives + checksums
                                    └── Update Homebrew tap formula
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
make tag V=0.1.0
```

This creates an annotated tag `v0.1.0` and pushes it to origin. Equivalent to:

```bash
git tag -a v0.1.0 -m "Release v0.1.0"
git push origin v0.1.0
```

### 3. Monitor the release

The [Release workflow](https://github.com/blackpaw-studio/leo/actions/workflows/release.yml) runs automatically. When it completes, the [Releases page](https://github.com/blackpaw-studio/leo/releases) will have:

- Release notes auto-generated from commit history
- Archives for each platform: `leo_0.1.0_darwin_amd64.tar.gz`, etc.
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

## Homebrew

Releases automatically update the formula in [`blackpaw-studio/homebrew-tap`](https://github.com/blackpaw-studio/homebrew-tap). After a release, users can install or upgrade with:

```bash
brew install blackpaw-studio/tap/leo
brew upgrade leo
```

## Testing Locally

To verify the GoReleaser configuration without publishing:

```bash
make snapshot
```

This creates a full build in `dist/` without pushing anything.

## Setup (Maintainers)

The release workflow requires one repository secret:

| Secret | Purpose |
|--------|---------|
| `HOMEBREW_TAP_TOKEN` | GitHub PAT with `repo` scope for pushing to the Homebrew tap repo |

`GITHUB_TOKEN` is provided automatically by GitHub Actions and handles release creation.

## Troubleshooting

**CI fails on tag push**
:   Fix the issue on `main`, delete the tag, and re-tag:

    ```bash
    git tag -d v0.1.0
    git push origin :refs/tags/v0.1.0
    # fix and push to main
    make tag V=0.1.0
    ```

**Homebrew tap not updated**
:   Verify the `HOMEBREW_TAP_TOKEN` secret is set and has write access to `blackpaw-studio/homebrew-tap`.

**GoReleaser config errors**
:   Validate locally before tagging:

    ```bash
    goreleaser check
    make snapshot
    ```
