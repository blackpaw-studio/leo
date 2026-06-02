# Design: `leo update --unstable` (install a build from main)

**Date:** 2026-06-02
**Status:** Approved, pending implementation plan

## Problem

There is no easy way to install the latest `main` HEAD build. Today `leo update`
installs the latest tagged stable release, and `leo update --pr <n>` installs a
PR build (uploaded by `prerelease.yml`, cosign-signed, fetched as a workflow
artifact and identity-verified). The "unreleased build" machinery exists but is
keyed to **PRs**, not **main**. We want `leo update --unstable` to grab the
newest passing build of `main`.

## Decisions (locked)

- **Flag:** `leo update --unstable` installs the newest passing `main` build.
- **Pinning:** `leo update --version main-<sha>` reinstalls an exact `main` build,
  mirroring the `--pr` / `pr-<n>-<sha>` flow.
- **Version label:** an installed unstable build reports `main-<sha>` from
  `leo version`.
- **Distribution:** artifact-based (workflow artifact via the GitHub API),
  identical token requirement to `--pr`. The GitHub Releases page is **untouched**.

## Non-goals (YAGNI)

- No Releases-page entry / rolling `edge` tag.
- No tokenless / curl-able install.
- No scheduled or automatic unstable updates.

## Architecture

### 1. Build pipeline — new `.github/workflows/unstable.yml`

A dedicated workflow (NOT an extension of `prerelease.yml`). Rationale:
`prerelease.yml` is thick with PR-only logic (sticky PR comment, fork-skip job,
concurrency keyed on PR number); adding a `push` trigger would require
conditionalizing nearly every step. A separate single-purpose workflow is
cleaner and matches the repo's "many small files / single responsibility" ethos.
The modest duplication (build + sign + upload, ~40 lines) is acceptable.

- Trigger: `push: { branches: [main] }`.
- Gates on `ci.yml` (reused, same as `prerelease.yml`).
- Build: goreleaser snapshot using a new `.goreleaser.unstable.yaml`, identical
  to `.goreleaser.prerelease.yaml` except
  `snapshot.version_template: "main-{{ .ShortCommit }}"`.
- Sign: cosign `sign-blob` over `checksums.txt`. The OIDC identity becomes
  `unstable.yml@refs/heads/main`.
- Upload: workflow artifact named `leo-unstable` (tarballs + checksums + sig +
  pem + metadata.json), `retention-days: 14`.
- `concurrency: { group: unstable-main, cancel-in-progress: true }` — only the
  newest main build matters.
- `permissions`: `contents: read`, `id-token: write` (cosign), `actions: read`.
  No `pull-requests: write`. No PR comment, no fork-skip job.

### 2. Signature verifier — `internal/update/signature.go`

Add:

```go
func SignatureVerifierForMain() (*SignatureVerifier, error) {
    return buildVerifierWithIdentity("unstable.yml", "refs/heads/main")
}
```

A one-liner mirroring `SignatureVerifierForPullRequest` and
`SignatureVerifierForVersion`. The existing `buildVerifierWithIdentity` already
composes owner/issuer/SAN; `refs/heads/main` needs no regex metacharacter
escaping but should be treated consistently with the existing ref patterns.

### 3. Resolver core — `internal/update/prerelease.go`

**Refactor (the one real structural change):** the download/verify/install core
is currently PR-flavored — `downloadAndReplaceFromRun`, `findPrereleaseArtifact`,
and `verifyPrereleaseSignature` hardcode the `leo-prerelease` artifact name and
the PR verifier factory. Generalize them to accept:

- an artifact name (`leo-prerelease` vs `leo-unstable`), and
- a verifier factory `func() (*SignatureVerifier, error)`.

Both the PR path and the main path then share one core. The PR-specific
`prNumber` threading in `downloadAndReplaceFromRun` collapses into the verifier
factory + a human-readable label used in log/error messages.

**Add for the main path:**

- `mainVersionPattern = regexp.MustCompile("^main-([0-9a-f]{7,40})$")`, plus
  `IsMainVersion(version string) bool` and
  `ParseMainVersion(version string) (shortSHA string, err error)`.
- `findLatestPassingMainRun(ctx, token, headSHA string) (workflowRun, error)` —
  queries `event=push&status=success&per_page=50` on `unstable.yml` (adds
  `&head_sha=` when pinning), returns the newest successful run. Because the
  workflow only runs on `main`, no branch/PR matching is needed beyond the query
  filter.
- `DownloadAndReplaceMain(ctx, opts) (path, version string, err error)` — resolve
  token → find latest passing main run → shared download/verify/install with the
  `leo-unstable` artifact name and `SignatureVerifierForMain`.
- `DownloadAndReplaceMainVersion(ctx, version, opts)` — parse `main-<sha>` →
  resolve full SHA via `resolveCommitSHA` → find run by `head_sha` → same core.

Mirror the existing var-injection seams (`prereleaseAPIBase`,
`prereleaseTokenSource`, an injectable main verifier factory) so tests can stub
the GitHub API and the verifier.

### 4. CLI — `internal/cli/update.go`

- Add `--unstable` bool flag → `runUnstableUpdate(allowUnsigned)` calling
  `update.DownloadAndReplaceMain`.
- Extend `--version` routing: `update.IsMainVersion(pinnedVersion)` routes to
  `runUnstableUpdateByVersion`. Broaden the current
  "`--version currently supports prerelease tags only`" error to also mention
  `main-<sha>`.
- Make `--pr`, `--unstable`, and `--version` mutually exclusive (extend the
  existing guard).
- Update the command long-help text to document `--unstable`.
- Factor a shared `unstableOptions(allowUnsigned)` helper paralleling
  `prereleaseOptions`. Both unstable paths end with `maybeRestartDaemon()`.

### 5. Version reporting & the `IsNewer` guard

The `Version` ldflag flows from the goreleaser snapshot template, so an installed
unstable build reports `main-<sha>` from `leo version` automatically — no extra
wiring.

**Correctness requirement:** a plain `leo update` from an unstable build must
still treat a real tagged release as newer, so `--unstable` is never a one-way
trap. `main-<sha>` is not valid semver. Confirm (and add a test asserting) that
`update.IsNewer("main-a1b2c3d", "1.4.0") == true` — i.e. a non-semver / unstable
current version is treated as older than any tagged release. If the current
`IsNewer` does not already guarantee this, add an explicit guard: an unparseable
current version is "older than" any parseable release.

### 6. Tests

- `internal/update`: mirror `prerelease_test.go` — httptest GitHub API stub + a
  synthesized `leo-unstable` artifact zip + stubbed `SignatureVerifierForMain`,
  covering both `DownloadAndReplaceMain` (latest) and
  `DownloadAndReplaceMainVersion` (pinned), plus `IsMainVersion` /
  `ParseMainVersion` edge cases and the no-successful-run error path.
- `internal/update`: the `IsNewer`-with-`main-<sha>` test from §5.
- `internal/cli`: flag-routing tests — `--unstable` calls the main path,
  `--version main-<sha>` routes to the version path, and the
  `--pr`/`--unstable`/`--version` mutual-exclusion guard.

## Files touched

| File | Change |
|------|--------|
| `.github/workflows/unstable.yml` | new — push-on-main build/sign/upload |
| `.goreleaser.unstable.yaml` | new — snapshot template `main-{{ .ShortCommit }}` |
| `internal/update/signature.go` | add `SignatureVerifierForMain` |
| `internal/update/prerelease.go` | generalize core; add main version parsing, run finder, `DownloadAndReplaceMain[Version]` |
| `internal/update/prerelease_test.go` (or new `*_test.go`) | main-path tests |
| `internal/update/update_test.go` | `IsNewer` non-semver guard test |
| `internal/cli/update.go` | `--unstable` flag, `--version main-…` routing, mutual exclusion, help |
| `internal/cli/update_test.go` | CLI routing tests |
| `docs/configuration/` (optional) | document `--unstable` alongside `--pr` |
