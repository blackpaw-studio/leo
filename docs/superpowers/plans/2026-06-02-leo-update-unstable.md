# `leo update --unstable` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `leo update --unstable` (and `leo update --version main-<sha>`) to install the newest passing build of `main`, reusing the existing PR-prerelease artifact/sign/verify machinery.

**Architecture:** A new `unstable.yml` workflow builds + cosign-signs a goreleaser snapshot on every push to `main` and uploads a `leo-unstable` workflow artifact. The `internal/update` package's PR resolver core is generalized to accept an artifact name + verifier factory, then a main-branch path (`DownloadAndReplaceMain` / `DownloadAndReplaceMainVersion`) is added alongside the PR path. The CLI gains a `--unstable` flag and `main-<sha>` `--version` routing.

**Tech Stack:** Go, Cobra, goreleaser (snapshot), cosign keyless (Sigstore/Fulcio OIDC), GitHub Actions, GitHub REST API.

---

## Preflight

- [ ] **Step 0: Branch off main**

Run:
```bash
cd /Users/evan/.leo/agents/leo
git checkout -b feat/update-unstable
```
Expected: `Switched to a new branch 'feat/update-unstable'`

---

## File Structure

| File | Responsibility |
|------|----------------|
| `.goreleaser.unstable.yaml` (new) | goreleaser snapshot config; `version_template: main-{{ .ShortCommit }}` |
| `.github/workflows/unstable.yml` (new) | push-on-main → CI gate → build → cosign sign → upload `leo-unstable` |
| `internal/update/signature.go` (modify) | add `SignatureVerifierForMain` |
| `internal/update/prerelease.go` (modify) | `mainVersionPattern`, `IsMainVersion`, `ParseMainVersion`, generalized core (`buildSource`, `findRunArtifact`, `verifyBundleSignature`, `downloadAndReplaceFromRun`), `findLatestPassingMainRun`, `DownloadAndReplaceMain[Version]`, `unstableArtifactName`, `unstableWorkflowFile`, `mainVerifier` seam |
| `internal/update/prerelease_test.go` (modify) | tests for the above (parsing, run finder, end-to-end) |
| `internal/update/update_test.go` (modify) | `IsNewer` regression test for `main-<sha>` |
| `internal/cli/update.go` (modify) | `--unstable` flag, `main-<sha>` `--version` routing, mutual exclusion, help text |
| `internal/cli/update_test.go` (modify) | CLI routing / mutual-exclusion tests |
| `docs/configuration/updating.md` (modify or new) | document `--unstable` next to `--pr` |

---

## Task 1: Generalize the resolver core (refactor, behavior-preserving)

This refactor introduces a `buildSource` describing which artifact + verifier + metadata-validation a download uses, so the PR and (future) main paths share one core. The PR path must keep behaving identically — existing PR tests are the safety net.

**Files:**
- Modify: `internal/update/prerelease.go` (functions `downloadAndReplaceFromRun`, `findPrereleaseArtifact`, `verifyPrereleaseSignature`)

- [ ] **Step 1: Run the existing PR tests to establish a green baseline**

Run: `go test ./internal/update/ -run 'PR|Prerelease|Artifact|Verifier' -count=1`
Expected: PASS (this is the behavior we must preserve).

- [ ] **Step 2: Add the `buildSource` type and an artifact-name constant**

Add near the top-level constants in `internal/update/prerelease.go` (below the existing `prereleaseArtifactName` const):

```go
// unstableArtifactName is the well-known artifact the unstable workflow
// uploads for main-branch builds, mirroring prereleaseArtifactName.
const unstableArtifactName = "leo-unstable"

// unstableWorkflowFile is the workflow filename that produces main builds.
// Used to filter workflow runs and to construct the cosign SAN identity.
const unstableWorkflowFile = "unstable.yml"
```

Add the `buildSource` type (place it just above `downloadAndReplaceFromRun`):

```go
// buildSource captures everything that differs between the PR-prerelease
// flow and the main-branch "unstable" flow: which artifact to pull, a
// human-readable label for messages, the cosign verifier to demand, and
// a hook to validate the artifact's embedded version against what the
// caller requested. The download/verify/install core is otherwise shared.
type buildSource struct {
	artifactName string
	label        string
	verifier     func() (*SignatureVerifier, error)
	validate     func(bundleVersion string) error
}
```

- [ ] **Step 3: Parameterize the artifact finder**

Rename `findPrereleaseArtifact` to `findRunArtifact` and add an artifact-name parameter. Replace the function:

```go
func findRunArtifact(ctx context.Context, token string, runID int64, artifactName string) (int64, error) {
	u := fmt.Sprintf("%s/actions/runs/%d/artifacts?per_page=100", prereleaseAPIBase, runID)
	body, err := githubAPIGet(ctx, token, u)
	if err != nil {
		return 0, fmt.Errorf("listing artifacts for run %d: %w", runID, err)
	}
	var resp artifactsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("decoding artifacts: %w", err)
	}
	for _, a := range resp.Artifacts {
		if a.Name != artifactName {
			continue
		}
		if a.Expired {
			return 0, fmt.Errorf("artifact %q on run %d has expired (artifacts are retained for ~14 days)", a.Name, runID)
		}
		return a.ID, nil
	}
	return 0, fmt.Errorf("no %q artifact found on run %d", artifactName, runID)
}
```

- [ ] **Step 4: Generalize the signature verification helper**

Replace `verifyPrereleaseSignature` with a label/verifier-parameterized version:

```go
// verifyBundleSignature is the shared cosign gate for both the PR and the
// main-branch flows. label is used in warnings; makeVerifier pins the
// expected OIDC identity for the relevant workflow + ref.
func verifyBundleSignature(label string, makeVerifier func() (*SignatureVerifier, error), bundle artifactBundle, opts PrereleaseOptions) error {
	if len(bundle.signature) == 0 || len(bundle.certificate) == 0 {
		if !opts.AllowUnsigned {
			return fmt.Errorf("artifact is missing %s or %s — refusing to update; "+
				"rerun with --allow-unsigned to fall back to SHA-only verification",
				signatureFileName, certFileName)
		}
		if opts.Warn != nil {
			opts.Warn("WARNING: %s has no cosign signature; relying on SHA-256 only.", label)
		}
		return nil
	}

	verifier, err := makeVerifier()
	if err != nil {
		if opts.AllowUnsigned {
			if opts.Warn != nil {
				opts.Warn("WARNING: signature verifier unavailable (%v); falling back to SHA-256 only.", err)
			}
			return nil
		}
		return fmt.Errorf("building verifier: %w", err)
	}
	return verifier.Verify(bundle.checksums, bundle.signature, bundle.certificate)
}
```

- [ ] **Step 5: Rewrite `downloadAndReplaceFromRun` to take a `buildSource`**

Replace the function signature and body's PR-specific parts:

```go
func downloadAndReplaceFromRun(ctx context.Context, token string, run workflowRun, src buildSource, opts PrereleaseOptions) (string, string, error) {
	binaryPath, err := osExecutable()
	if err != nil {
		return "", "", fmt.Errorf("finding current binary: %w", err)
	}
	binaryPath, err = filepath.EvalSymlinks(binaryPath)
	if err != nil {
		return "", "", fmt.Errorf("resolving binary path: %w", err)
	}

	artifactID, err := findRunArtifact(ctx, token, run.ID, src.artifactName)
	if err != nil {
		return "", "", err
	}

	zipBytes, err := downloadArtifactZip(ctx, token, artifactID)
	if err != nil {
		return "", "", err
	}

	bundle, err := readArtifactBundle(zipBytes)
	if err != nil {
		return "", "", fmt.Errorf("reading artifact: %w", err)
	}

	if bundle.version == "" {
		return "", "", errors.New("artifact bundle is missing metadata.json with a version field")
	}
	if err := src.validate(bundle.version); err != nil {
		return "", "", err
	}

	if err := verifyBundleSignature(src.label, src.verifier, bundle, opts); err != nil {
		return "", "", fmt.Errorf("verifying %s signature: %w", src.label, err)
	}

	archiveName := fmt.Sprintf("leo_%s_%s_%s.tar.gz", bundle.version, runtime.GOOS, runtime.GOARCH)
	archive, ok := bundle.archives[archiveName]
	if !ok {
		return "", "", fmt.Errorf("no archive for current platform %s in artifact bundle (expected %s)", runtime.GOOS+"_"+runtime.GOARCH, archiveName)
	}

	expected, err := parseChecksum(string(bundle.checksums), archiveName)
	if err != nil {
		return "", "", err
	}
	if err := verifyArchiveChecksumAgainst(archiveName, archive, expected); err != nil {
		return "", "", fmt.Errorf("verifying %s: %w", archiveName, err)
	}

	if err := installBinary(binaryPath, archive); err != nil {
		return "", "", err
	}
	return binaryPath, bundle.version, nil
}
```

- [ ] **Step 6: Update the two PR callers to build a `buildSource`**

In `DownloadAndReplacePR`, replace the final line `return downloadAndReplaceFromRun(ctx, token, prNumber, run, opts)` with:

```go
	return downloadAndReplaceFromRun(ctx, token, run, prBuildSource(prNumber), opts)
```

In `DownloadAndReplacePRVersion`, replace its final `return downloadAndReplaceFromRun(...)` line the same way:

```go
	return downloadAndReplaceFromRun(ctx, token, run, prBuildSource(prNumber), opts)
```

Add the `prBuildSource` constructor (place it directly below `DownloadAndReplacePRVersion`):

```go
// prBuildSource describes the PR-prerelease artifact + verifier + metadata
// check for downloadAndReplaceFromRun.
func prBuildSource(prNumber int) buildSource {
	return buildSource{
		artifactName: prereleaseArtifactName,
		label:        fmt.Sprintf("PR #%d", prNumber),
		verifier:     func() (*SignatureVerifier, error) { return prereleaseVerifierForPR(prNumber) },
		validate: func(version string) error {
			n, _, err := ParsePrereleaseVersion(version)
			if err != nil {
				return fmt.Errorf("artifact metadata: %w", err)
			}
			if n != prNumber {
				return fmt.Errorf("artifact metadata reports PR #%d but we requested PR #%d", n, prNumber)
			}
			return nil
		},
	}
}
```

- [ ] **Step 7: Run the full update package tests to confirm no behavior change**

Run: `go test ./internal/update/ -count=1`
Expected: PASS (all existing PR / artifact / verifier tests still green).

- [ ] **Step 8: Commit**

```bash
git add internal/update/prerelease.go
git commit -m "refactor(update): generalize prerelease resolver core via buildSource"
```

---

## Task 2: `SignatureVerifierForMain`

**Files:**
- Modify: `internal/update/signature.go`
- Test: `internal/update/prerelease_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/update/prerelease_test.go`:

```go
func TestSignatureVerifierForMain_PinsIdentity(t *testing.T) {
	v, err := SignatureVerifierForMain()
	if err != nil {
		t.Fatalf("SignatureVerifierForMain: %v", err)
	}
	want := "https://github.com/blackpaw-studio/leo/.github/workflows/unstable.yml@refs/heads/main"
	if !v.SANRegex.MatchString(want) {
		t.Errorf("SAN regex %q did not match expected identity %q", v.SANRegex.String(), want)
	}
	// Must NOT match a PR identity or the release identity.
	for _, bad := range []string{
		"https://github.com/blackpaw-studio/leo/.github/workflows/prerelease.yml@refs/pull/7/merge",
		"https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/v1.0.0",
		"https://github.com/blackpaw-studio/leo/.github/workflows/unstable.yml@refs/heads/feature",
	} {
		if v.SANRegex.MatchString(bad) {
			t.Errorf("SAN regex must not match %q", bad)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/update/ -run TestSignatureVerifierForMain_PinsIdentity -count=1`
Expected: FAIL — `undefined: SignatureVerifierForMain`.

- [ ] **Step 3: Implement**

Add to `internal/update/signature.go`, directly below `SignatureVerifierForPullRequest`:

```go
// SignatureVerifierForMain builds a verifier pinned to the unstable
// workflow's OIDC identity for main-branch builds. Those signatures are
// issued by `unstable.yml@refs/heads/main`; pinning the ref to the exact
// `main` branch (not an arbitrary head) closes the same downgrade window
// SignatureVerifierForVersion closes for tagged releases.
func SignatureVerifierForMain() (*SignatureVerifier, error) {
	return buildVerifierWithIdentity(unstableWorkflowFile, `refs/heads/main`)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/update/ -run TestSignatureVerifierForMain_PinsIdentity -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/update/signature.go internal/update/prerelease_test.go
git commit -m "feat(update): add SignatureVerifierForMain for unstable builds"
```

---

## Task 3: Main version parsing (`IsMainVersion`, `ParseMainVersion`)

**Files:**
- Modify: `internal/update/prerelease.go`
- Test: `internal/update/prerelease_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/update/prerelease_test.go`:

```go
func TestIsMainVersion(t *testing.T) {
	cases := map[string]bool{
		"main-a1b2c3d":                              true,
		"main-a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9": true, // 40-char full sha
		"main-a1b2c3":                               false, // <7 hex
		"main-A1B2C3D":                              false, // uppercase
		"main-":                                     false,
		"pr-42-a1b2c3d":                             false,
		"v1.2.3":                                    false,
		"":                                          false,
	}
	for in, want := range cases {
		if got := IsMainVersion(in); got != want {
			t.Errorf("IsMainVersion(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseMainVersion(t *testing.T) {
	sha, err := ParseMainVersion("main-a1b2c3d")
	if err != nil {
		t.Fatalf("ParseMainVersion: %v", err)
	}
	if sha != "a1b2c3d" {
		t.Errorf("sha = %q, want a1b2c3d", sha)
	}
	if _, err := ParseMainVersion("pr-1-a1b2c3d"); err == nil {
		t.Error("expected error for non-main version")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/update/ -run 'TestIsMainVersion|TestParseMainVersion' -count=1`
Expected: FAIL — `undefined: IsMainVersion`.

- [ ] **Step 3: Implement**

Add to `internal/update/prerelease.go`, directly below the `prereleaseVersionPattern` var and its `IsPrereleaseVersion` / `ParsePrereleaseVersion` helpers:

```go
// mainVersionPattern matches version strings produced by the unstable
// workflow's goreleaser snapshot template:
//
//	main-<7+ hex chars>
//
// Used by the CLI to route a --version flag through the main flow.
var mainVersionPattern = regexp.MustCompile(`^main-([0-9a-f]{7,40})$`)

// IsMainVersion reports whether a version string targets a main build.
func IsMainVersion(version string) bool {
	return mainVersionPattern.MatchString(version)
}

// ParseMainVersion extracts the short SHA from a "main-<sha>" version
// string. Returns an error if the shape doesn't match.
func ParseMainVersion(version string) (shortSHA string, err error) {
	m := mainVersionPattern.FindStringSubmatch(version)
	if m == nil {
		return "", fmt.Errorf("version %q is not a main build tag (want main-<sha>)", version)
	}
	return m[1], nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/update/ -run 'TestIsMainVersion|TestParseMainVersion' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/update/prerelease.go internal/update/prerelease_test.go
git commit -m "feat(update): add main-<sha> version parsing"
```

---

## Task 4: `findLatestPassingMainRun`

**Files:**
- Modify: `internal/update/prerelease.go`
- Test: `internal/update/prerelease_test.go`

Note: the existing test helper `newFakeGitHubAPI(t) *fakeGitHubAPI` (in `prerelease_test.go`) creates an httptest server, points `prereleaseAPIBase` at it, and registers cleanup via `t.Cleanup` (no manual `close()`). Register routes with its `api.handle(path, fn)` method, which **enforces an `Authorization: Bearer test-token` header** — so tests must pass the token `"test-token"`. Encode responses with the real `workflowRunsResponse` / `workflowRun` structs.

- [ ] **Step 1: Write the failing test**

Add to `internal/update/prerelease_test.go`:

```go
func TestFindLatestPassingMainRun_PicksNewestSuccess(t *testing.T) {
	api := newFakeGitHubAPI(t)
	api.handle("/actions/workflows/unstable.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("event"); got != "push" {
			t.Errorf("event query = %q, want push", got)
		}
		_ = json.NewEncoder(w).Encode(workflowRunsResponse{
			WorkflowRuns: []workflowRun{
				{ID: 222, Conclusion: "success"},
				{ID: 111, Conclusion: "success"},
			},
		})
	})

	run, err := findLatestPassingMainRun(context.Background(), "test-token", "")
	if err != nil {
		t.Fatalf("findLatestPassingMainRun: %v", err)
	}
	if run.ID != 222 {
		t.Errorf("run.ID = %d, want 222 (newest)", run.ID)
	}
}

func TestFindLatestPassingMainRun_NoRunsReturnsError(t *testing.T) {
	api := newFakeGitHubAPI(t)
	api.handle("/actions/workflows/unstable.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(workflowRunsResponse{})
	})
	if _, err := findLatestPassingMainRun(context.Background(), "test-token", ""); err == nil {
		t.Error("expected error when no successful main run exists")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/update/ -run TestFindLatestPassingMainRun -count=1`
Expected: FAIL — `undefined: findLatestPassingMainRun`.

- [ ] **Step 3: Implement**

Add to `internal/update/prerelease.go`, directly below `findLatestPassingPRRun`:

```go
// findLatestPassingMainRun returns the newest successful run of the
// unstable workflow on main. GitHub returns runs newest-first; we take
// the first success. When headSHA is non-empty (pinned --version path)
// the query is narrowed to that commit.
func findLatestPassingMainRun(ctx context.Context, token, headSHA string) (workflowRun, error) {
	u := fmt.Sprintf("%s/actions/workflows/%s/runs?event=push&status=success&per_page=50",
		prereleaseAPIBase, unstableWorkflowFile)
	if headSHA != "" {
		u += "&head_sha=" + url.QueryEscape(headSHA)
	}

	body, err := githubAPIGet(ctx, token, u)
	if err != nil {
		return workflowRun{}, fmt.Errorf("listing workflow runs: %w", err)
	}
	var resp workflowRunsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return workflowRun{}, fmt.Errorf("decoding workflow runs: %w", err)
	}

	for _, run := range resp.WorkflowRuns {
		if run.Conclusion == "success" {
			return run, nil
		}
	}

	if headSHA != "" {
		short := headSHA
		if len(short) > 7 {
			short = short[:7]
		}
		return workflowRun{}, fmt.Errorf("no successful main build at commit %s (the build may have failed or expired)", short)
	}
	return workflowRun{}, fmt.Errorf("no successful main build found (has the unstable workflow run yet?)")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/update/ -run TestFindLatestPassingMainRun -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/update/prerelease.go internal/update/prerelease_test.go
git commit -m "feat(update): add findLatestPassingMainRun"
```

---

## Task 5: `DownloadAndReplaceMain` and `DownloadAndReplaceMainVersion`

**Files:**
- Modify: `internal/update/prerelease.go`
- Test: `internal/update/prerelease_test.go`

Note: model the end-to-end test exactly on `TestDownloadAndReplacePR_EndToEnd`. That test does **not** stub a verifier — it omits the sig/cert files and rides `AllowUnsigned: true` so the cosign gate is bypassed cleanly (identity matching is covered by Task 2's `TestSignatureVerifierForMain_PinsIdentity`). Real fixtures it uses: `tarGzWithLeo(t, []byte)` (builds the tarball), `buildArtifactZip(t, map[string][]byte{...})` (zips a name→bytes map), inline `osExecutable` stub via `t.Cleanup`, `newFakeGitHubAPI(t)` + `api.handle(...)` (auth = `Bearer test-token`), and the artifact-zip route `/actions/artifacts/<id>/zip` (the resolver builds that URL from `prereleaseAPIBase`).

- [ ] **Step 1: Add the production verifier seam**

Add to `internal/update/prerelease.go`, next to the existing `prereleaseVerifierForPR` var. (Used by `mainBuildSource` in Step 4; the end-to-end test rides `AllowUnsigned` and does not stub it, but the var must exist for the production path.)

```go
// mainVerifier is the cosign identity factory for main builds, injected
// here so tests can stub it the same way prereleaseVerifierForPR is
// stubbed for the PR flow.
var mainVerifier = SignatureVerifierForMain
```

- [ ] **Step 2: Write the failing end-to-end test**

Add to `internal/update/prerelease_test.go`. This mirrors `TestDownloadAndReplacePR_EndToEnd`; differences are: artifact name `leo-unstable`, metadata version `main-a1b2c3d`, archive name `leo_main-a1b2c3d_...`, runs endpoint `/actions/workflows/unstable.yml/runs`, and the entry point `DownloadAndReplaceMain`.

```go
func TestDownloadAndReplaceMain_EndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	binaryPath := filepath.Join(tmpDir, "leo")
	if err := os.WriteFile(binaryPath, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolvedBinaryPath, err := filepath.EvalSymlinks(binaryPath)
	if err != nil {
		t.Fatal(err)
	}

	prevExec := osExecutable
	osExecutable = func() (string, error) { return binaryPath, nil }
	t.Cleanup(func() { osExecutable = prevExec })

	// Omit sig/cert and ride AllowUnsigned, exactly like the PR end-to-end
	// test — identity matching is covered by TestSignatureVerifierForMain.
	newBinary := []byte("NEW-LEO-MAIN-BUILD")
	archive := tarGzWithLeo(t, newBinary)
	archiveName := fmt.Sprintf("leo_main-a1b2c3d_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(archive)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)

	zipBytes := buildArtifactZip(t, map[string][]byte{
		archiveName:     archive,
		"checksums.txt": []byte(checksums),
		"metadata.json": []byte(`{"version":"main-a1b2c3d"}`),
	})

	api := newFakeGitHubAPI(t)
	api.handle("/actions/workflows/unstable.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(workflowRunsResponse{
			WorkflowRuns: []workflowRun{{ID: 900, Conclusion: "success"}},
		})
	})
	api.handle("/actions/runs/900/artifacts", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(artifactsResponse{
			Artifacts: []artifact{{ID: 901, Name: "leo-unstable"}},
		})
	})
	api.handle("/actions/artifacts/901/zip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipBytes)
	})

	gotPath, gotVersion, err := DownloadAndReplaceMain(context.Background(), PrereleaseOptions{Token: "test-token", AllowUnsigned: true})
	if err != nil {
		t.Fatalf("DownloadAndReplaceMain: %v", err)
	}
	if gotPath != resolvedBinaryPath {
		t.Errorf("path = %q, want %q", gotPath, resolvedBinaryPath)
	}
	if gotVersion != "main-a1b2c3d" {
		t.Errorf("version = %q, want main-a1b2c3d", gotVersion)
	}
	body, err := os.ReadFile(resolvedBinaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, newBinary) {
		t.Errorf("binary contents = %q, want %q", body, newBinary)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/update/ -run TestDownloadAndReplaceMain_EndToEnd -count=1`
Expected: FAIL — `undefined: DownloadAndReplaceMain`.

- [ ] **Step 4: Implement both entry points and the main `buildSource`**

Add to `internal/update/prerelease.go`, directly below `DownloadAndReplacePRVersion` (and below `prBuildSource`):

```go
// DownloadAndReplaceMain installs the newest passing main build.
func DownloadAndReplaceMain(ctx context.Context, opts PrereleaseOptions) (string, string, error) {
	token, source, err := resolveToken(opts)
	if err != nil {
		return "", "", err
	}
	if opts.Warn != nil {
		opts.Warn("Authenticating to GitHub via %s.", source)
	}

	run, err := findLatestPassingMainRun(ctx, token, "")
	if err != nil {
		return "", "", err
	}
	return downloadAndReplaceFromRun(ctx, token, run, mainBuildSource(""), opts)
}

// DownloadAndReplaceMainVersion installs a specific main-<sha> build.
func DownloadAndReplaceMainVersion(ctx context.Context, version string, opts PrereleaseOptions) (string, string, error) {
	shortSHA, err := ParseMainVersion(version)
	if err != nil {
		return "", "", err
	}

	token, source, err := resolveToken(opts)
	if err != nil {
		return "", "", err
	}
	if opts.Warn != nil {
		opts.Warn("Authenticating to GitHub via %s.", source)
	}

	fullSHA, err := resolveCommitSHA(ctx, token, shortSHA)
	if err != nil {
		return "", "", fmt.Errorf("resolving commit %s: %w", shortSHA, err)
	}

	run, err := findLatestPassingMainRun(ctx, token, fullSHA)
	if err != nil {
		return "", "", err
	}
	return downloadAndReplaceFromRun(ctx, token, run, mainBuildSource(version), opts)
}

// mainBuildSource describes the unstable artifact + verifier + metadata
// check. When wantVersion is non-empty (pinned path) the bundle's version
// must match exactly; otherwise any well-formed main-<sha> is accepted.
func mainBuildSource(wantVersion string) buildSource {
	return buildSource{
		artifactName: unstableArtifactName,
		label:        "main build",
		verifier:     mainVerifier,
		validate: func(version string) error {
			if !IsMainVersion(version) {
				return fmt.Errorf("artifact metadata version %q is not a main build", version)
			}
			if wantVersion != "" && version != wantVersion {
				return fmt.Errorf("artifact metadata reports %s but we requested %s", version, wantVersion)
			}
			return nil
		},
	}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/update/ -run TestDownloadAndReplaceMain_EndToEnd -count=1`
Expected: PASS.

- [ ] **Step 6: Run the full update package suite**

Run: `go test ./internal/update/ -count=1`
Expected: PASS (PR + main paths both green).

- [ ] **Step 7: Commit**

```bash
git add internal/update/prerelease.go internal/update/prerelease_test.go
git commit -m "feat(update): add DownloadAndReplaceMain[Version] for main builds"
```

---

## Task 6: `IsNewer` regression test for `main-<sha>`

No production change — `parseVersion("main-a1b2c3d")` strips at the hyphen to `"main"` → `[0,0,0]`, so a tagged release already compares as newer. This test locks that in so `--unstable` is never a one-way trap.

**Files:**
- Test: `internal/update/update_test.go`

- [ ] **Step 1: Write the test**

Add to `internal/update/update_test.go`:

```go
func TestIsNewer_MainBuildIsOlderThanRelease(t *testing.T) {
	// An installed unstable build must still see a real release as newer,
	// so `leo update` from a main build is never a dead end.
	if !IsNewer("main-a1b2c3d", "1.4.0") {
		t.Error("IsNewer(main-<sha>, 1.4.0) = false, want true")
	}
	if !IsNewer("main-a1b2c3d", "0.0.1") {
		t.Error("IsNewer(main-<sha>, 0.0.1) = false, want true")
	}
}
```

- [ ] **Step 2: Run the test**

Run: `go test ./internal/update/ -run TestIsNewer_MainBuildIsOlderThanRelease -count=1`
Expected: PASS (no implementation needed).

- [ ] **Step 3: Commit**

```bash
git add internal/update/update_test.go
git commit -m "test(update): lock in main build < tagged release for IsNewer"
```

---

## Task 7: CLI — `--unstable` flag, `main-<sha>` routing, mutual exclusion

**Files:**
- Modify: `internal/cli/update.go`
- Test: `internal/cli/update_test.go`

Read `internal/cli/update_test.go` first to match its existing test style (it tests `--pr`/`--version` routing and mutual exclusion). Add parallel cases.

- [ ] **Step 1: Write the failing tests**

Add to `internal/cli/update_test.go` (mirror the existing `--pr`-vs-`--version` mutual-exclusion test's structure and helper, whatever it is named):

```go
func TestUpdateCmd_UnstableAndPRMutuallyExclusive(t *testing.T) {
	cmd := newUpdateCmd()
	cmd.SetArgs([]string{"--unstable", "--pr", "5"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutual-exclusion error, got %v", err)
	}
}

func TestUpdateCmd_UnstableAndVersionMutuallyExclusive(t *testing.T) {
	cmd := newUpdateCmd()
	cmd.SetArgs([]string{"--unstable", "--version", "main-a1b2c3d"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutual-exclusion error, got %v", err)
	}
}
```

> If `newUpdateCmd()` executes real network/update logic on a valid flag combo, these tests must only exercise the *guard* (which returns before any network call). The two arg combos above all trip the guard first, so no stubbing is needed. If the existing PR mutual-exclusion test uses a different invocation pattern (e.g. calling `RunE` directly), match it.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/ -run 'TestUpdateCmd_Unstable' -count=1`
Expected: FAIL — `--unstable` flag unknown / guard not present.

- [ ] **Step 3: Add the flag and routing in `internal/cli/update.go`**

In `newUpdateCmd`, add the flag variable beside the others:

```go
	var unstable bool
```

Register the flag (beside the existing `--pr` / `--version` registrations):

```go
	cmd.Flags().BoolVar(&unstable, "unstable", false,
		"install the most recent passing main build (requires a GitHub token)")
```

Replace the start of the `RunE` body's flag-routing block. The existing block begins:

```go
			if prNumber > 0 && pinnedVersion != "" {
				return fmt.Errorf("--pr and --version are mutually exclusive")
			}
			if prNumber > 0 {
				return runPrereleaseUpdateByPR(prNumber, allowUnsigned)
			}
			if update.IsPrereleaseVersion(pinnedVersion) {
				return runPrereleaseUpdateByVersion(pinnedVersion, allowUnsigned)
			}
			if pinnedVersion != "" {
				return fmt.Errorf("--version currently supports prerelease tags only (pr-<n>-<sha>); to install a tagged release, omit --version and let leo find the latest")
			}
```

Replace it with:

```go
			selected := 0
			if prNumber > 0 {
				selected++
			}
			if unstable {
				selected++
			}
			if pinnedVersion != "" {
				selected++
			}
			if selected > 1 {
				return fmt.Errorf("--pr, --unstable, and --version are mutually exclusive")
			}
			if prNumber > 0 {
				return runPrereleaseUpdateByPR(prNumber, allowUnsigned)
			}
			if unstable {
				return runUnstableUpdate(allowUnsigned)
			}
			if update.IsPrereleaseVersion(pinnedVersion) {
				return runPrereleaseUpdateByVersion(pinnedVersion, allowUnsigned)
			}
			if update.IsMainVersion(pinnedVersion) {
				return runUnstableUpdateByVersion(pinnedVersion, allowUnsigned)
			}
			if pinnedVersion != "" {
				return fmt.Errorf("--version currently supports prerelease tags only (pr-<n>-<sha> or main-<sha>); to install a tagged release, omit --version and let leo find the latest")
			}
```

- [ ] **Step 4: Add the two run helpers**

Add to `internal/cli/update.go`, directly below `runPrereleaseUpdateByVersion`:

```go
// runUnstableUpdate installs the latest passing main build.
func runUnstableUpdate(allowUnsigned bool) error {
	opts := prereleaseOptions(allowUnsigned)
	info.Println("Installing latest main build...")
	path, version, err := update.DownloadAndReplaceMain(context.Background(), opts)
	if err != nil {
		return fmt.Errorf("installing main build: %w", err)
	}
	success.Printf("Updated %s to %s\n", path, version)
	return maybeRestartDaemon()
}

// runUnstableUpdateByVersion installs a specific main-<sha> build.
func runUnstableUpdateByVersion(version string, allowUnsigned bool) error {
	opts := prereleaseOptions(allowUnsigned)
	info.Printf("Installing main build %s...\n", version)
	path, installedVersion, err := update.DownloadAndReplaceMainVersion(context.Background(), version, opts)
	if err != nil {
		return fmt.Errorf("installing %s: %w", version, err)
	}
	success.Printf("Updated %s to %s\n", path, installedVersion)
	return maybeRestartDaemon()
}
```

- [ ] **Step 5: Update the command long-help text**

In `newUpdateCmd`, the `Long:` string currently ends with the `--pr`/`--version` paragraph. Append a sentence about `--unstable`. Replace:

```go
			"Pass --pr <n> to install the most recent successful PR build\n" +
			"(uploaded by the prerelease workflow), or --version pr-<n>-<sha>\n" +
			"to pin to an exact PR build. Both forms need a GitHub token; the\n" +
			"command tries the gh CLI first, then $GH_TOKEN / $GITHUB_TOKEN /\n" +
			"$LEO_GITHUB_TOKEN.",
```

with:

```go
			"Pass --pr <n> to install the most recent successful PR build\n" +
			"(uploaded by the prerelease workflow), or --version pr-<n>-<sha>\n" +
			"to pin to an exact PR build. Pass --unstable to install the most\n" +
			"recent passing build of main, or --version main-<sha> to pin to an\n" +
			"exact main build. All of these need a GitHub token; the command\n" +
			"tries the gh CLI first, then $GH_TOKEN / $GITHUB_TOKEN /\n" +
			"$LEO_GITHUB_TOKEN.",
```

- [ ] **Step 6: Run the CLI tests**

Run: `go test ./internal/cli/ -run 'TestUpdateCmd' -count=1`
Expected: PASS.

- [ ] **Step 7: Build and smoke-test the help text**

Run:
```bash
make build && ./bin/leo update --help
```
Expected: build succeeds; help output lists `--unstable` and mentions `main-<sha>`.

- [ ] **Step 8: Commit**

```bash
git add internal/cli/update.go internal/cli/update_test.go
git commit -m "feat(cli): add leo update --unstable and main-<sha> pinning"
```

---

## Task 8: goreleaser unstable config

**Files:**
- Create: `.goreleaser.unstable.yaml`

- [ ] **Step 1: Create the config**

Create `.goreleaser.unstable.yaml` (identical to `.goreleaser.prerelease.yaml` except the snapshot template and the header comment):

```yaml
version: 2

project_name: leo

# Unstable config for main-branch builds. Identical to
# .goreleaser.prerelease.yaml except snapshot.version_template produces a
# `main-<short-sha>` version so the embedded ldflag and archive name carry
# the commit identity. Artifacts are uploaded as workflow artifacts (not a
# GitHub Release); the workflow signs checksums.txt explicitly via cosign
# sign-blob against the unstable workflow's OIDC identity.

builds:
  - main: ./cmd/leo
    binary: leo
    ldflags:
      - -s -w -X github.com/blackpaw-studio/leo/internal/cli.Version={{.Version}}
    goos:
      - darwin
      - linux
    goarch:
      - amd64
      - arm64
    env:
      - CGO_ENABLED=0

archives:
  - formats: [tar.gz]
    name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"

checksum:
  name_template: checksums.txt

snapshot:
  version_template: "main-{{ .ShortCommit }}"

changelog:
  disable: true

release:
  disable: true
```

- [ ] **Step 2: Validate the config**

Run: `goreleaser check --config .goreleaser.unstable.yaml`
Expected: `config is valid` (no errors).

- [ ] **Step 3: Verify the snapshot version template produces `main-<sha>`**

Run:
```bash
goreleaser build --snapshot --clean --config .goreleaser.unstable.yaml --single-target -o /tmp/leo-unstable-smoke && /tmp/leo-unstable-smoke version
```
Expected: prints a version like `main-<7hex>` (the `version` subcommand reports the ldflag-injected `Version`). If `leo version` output differs, confirm `internal/cli.Version` is what `version` prints.

- [ ] **Step 4: Commit**

```bash
git add .goreleaser.unstable.yaml
git commit -m "build: add goreleaser unstable config (main-<sha> snapshots)"
```

---

## Task 9: `unstable.yml` workflow

**Files:**
- Create: `.github/workflows/unstable.yml`

- [ ] **Step 1: Create the workflow**

Create `.github/workflows/unstable.yml`. This mirrors `prerelease.yml`'s build job but is triggered by push-to-main, drops all PR-comment / fork-skip logic, and signs against `unstable.yml@refs/heads/main`:

```yaml
name: Unstable

# Push-to-main builds: goreleaser snapshot -> cosign sign -> workflow
# artifact upload. Installed via `leo update --unstable`.
#
# Like prerelease.yml, this never publishes to GitHub Releases — the
# Releases page stays a surface for tagged stable releases only. Unlike
# prerelease.yml there is no PR comment and no fork handling (push events
# only fire on the canonical repo).

on:
  push:
    branches: [main]

permissions:
  contents: read

concurrency:
  group: unstable-main
  cancel-in-progress: true

jobs:
  ci:
    uses: ./.github/workflows/ci.yml

  build:
    name: Build unstable artifact
    needs: ci
    runs-on: ubuntu-latest
    permissions:
      contents: read
      id-token: write   # cosign keyless signing
      actions: read      # read artifacts/runs API
    steps:
      - uses: actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd # v6.0.2
        with:
          fetch-depth: 0

      - uses: actions/setup-go@4a3601121dd01d1626a1e23e37211e3254c1c06c # v6.4.0
        with:
          go-version-file: go.mod
          cache: true

      - name: Install cosign
        uses: sigstore/cosign-installer@dc72c7d5c4d10cd6bcb8cf6e3fd625a9e5e537da # v3.7.0

      - name: Build with goreleaser
        uses: goreleaser/goreleaser-action@ec59f474b9834571250b370d4735c50f8e2d1e29 # v7.0.0
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        with:
          version: "~> v2"
          args: release --snapshot --clean --config .goreleaser.unstable.yaml

      - name: Sign checksums with cosign
        # Snapshot mode skips goreleaser's signs: block, so sign explicitly.
        # The OIDC token cosign requests carries
        # `unstable.yml@refs/heads/main` as its identity — that's what
        # `leo update --unstable` pins against.
        env:
          COSIGN_EXPERIMENTAL: "1"
        run: |
          set -eu
          cd dist
          cosign sign-blob \
            --yes \
            --output-signature=checksums.txt.sig \
            --output-certificate=checksums.txt.pem \
            checksums.txt

      - name: Upload artifact
        uses: actions/upload-artifact@v4
        with:
          name: leo-unstable
          path: |
            dist/*.tar.gz
            dist/checksums.txt
            dist/checksums.txt.sig
            dist/checksums.txt.pem
            dist/metadata.json
          if-no-files-found: error
          retention-days: 14
```

- [ ] **Step 2: Lint the workflow**

Run: `actionlint .github/workflows/unstable.yml`
Expected: no output (exit 0). If actionlint flags the `upload-artifact@v4` unpinned ref, match whatever pinning convention the other workflows use (`prerelease.yml` uses `actions/upload-artifact@v4` unpinned — keep parity).

- [ ] **Step 3: Sanity-check the goreleaser arg path matches the config filename**

Run: `grep -n "goreleaser.unstable.yaml" .github/workflows/unstable.yml .goreleaser.unstable.yaml`
Expected: the workflow's `--config .goreleaser.unstable.yaml` matches the file created in Task 8.

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/unstable.yml
git commit -m "ci: add unstable workflow (build+sign main HEAD, upload leo-unstable)"
```

---

## Task 10: Documentation

**Files:**
- Modify (or create): `docs/configuration/updating.md`

First check whether an updating/prerelease doc already exists; if `docs/configuration/persistent-tasks.md` is the pattern, follow that file's style. If a doc already documents `--pr`, extend it rather than creating a new file.

- [ ] **Step 1: Locate the existing update docs**

Run: `grep -rl "leo update --pr\|update --pr\|prerelease" docs/ || echo "no existing update doc"`
Expected: either a path to extend, or "no existing update doc" (then create `docs/configuration/updating.md`).

- [ ] **Step 2: Add the `--unstable` section**

Add (extending the existing `--pr` section, or in a new `docs/configuration/updating.md` mirroring the repo's doc style):

```markdown
## Installing a build from main (`--unstable`)

`leo update --unstable` installs the newest passing build of the `main`
branch — useful for trying a fix that's merged but not yet in a tagged
release. Every push to `main` triggers the `unstable.yml` workflow, which
builds a goreleaser snapshot, cosign-signs it, and uploads a `leo-unstable`
workflow artifact (retained 14 days).

```bash
leo update --unstable                 # newest passing main build
leo update --version main-a1b2c3d     # pin to an exact main build
```

Like `--pr`, this needs a GitHub token (the workflow-artifact API is
auth-gated): `leo` tries the `gh` CLI first, then `$GH_TOKEN`,
`$GITHUB_TOKEN`, and `$LEO_GITHUB_TOKEN`. The signing identity is
`unstable.yml@refs/heads/main`, which `leo update` verifies automatically.

Installed unstable builds report a `main-<sha>` version. A later
`leo update` to a tagged release always supersedes a main build, so
`--unstable` is never a dead end. These are not release builds; don't run
them in production.
```

- [ ] **Step 3: Commit**

```bash
git add docs/
git commit -m "docs: document leo update --unstable"
```

---

## Task 11: Full verification

- [ ] **Step 1: Full test suite with race detector**

Run: `make test`
Expected: PASS, no race warnings.

- [ ] **Step 2: Lint**

Run: `make lint`
Expected: clean (no go vet / staticcheck findings). If staticcheck is not installed locally, note it and rely on CI.

- [ ] **Step 3: Build**

Run: `make build`
Expected: `bin/leo` builds.

- [ ] **Step 4: Push and open the PR**

```bash
git push -u origin feat/update-unstable
gh pr create --fill --base main
```

Expected: PR opens. The new `unstable.yml` will only run on push-to-main (post-merge), so verify CI + the existing `prerelease.yml` pass on the PR. After merge, confirm the first `unstable.yml` run on main produces a `leo-unstable` artifact, then smoke-test `leo update --unstable` end-to-end.

---

## Self-Review Notes

- **Spec coverage:** §1 build pipeline → Tasks 8+9; §2 verifier → Task 2; §3 resolver core/refactor + parsing + run finder + entry points → Tasks 1,3,4,5; §4 CLI → Task 7; §5 version label (automatic via ldflag) + IsNewer guard → Task 6; §6 tests → embedded per task. All sections mapped.
- **Type consistency:** `buildSource{artifactName,label,verifier,validate}` defined Task 1, used Tasks 1 & 5. `mainVerifier` seam added Task 5 Step 1, used in `mainBuildSource`. `findLatestPassingMainRun(ctx, token, headSHA)` signature consistent Tasks 4 & 5. `DownloadAndReplaceMain(ctx, opts)` / `DownloadAndReplaceMainVersion(ctx, version, opts)` consistent Tasks 5 & 7.
- **Fixture helpers (verified against `prerelease_test.go`):** `newFakeGitHubAPI(t) *fakeGitHubAPI` + `api.handle(path, fn)` (auth = `Bearer test-token`, so tests use token `"test-token"`), `tarGzWithLeo(t, []byte)`, `buildArtifactZip(t, map[string][]byte)`, inline `osExecutable` stub via `t.Cleanup`, artifact-zip route `/actions/artifacts/<id>/zip`. End-to-end tests ride `AllowUnsigned: true` (omit sig/cert) rather than stubbing a verifier — identity is covered separately by Task 2.
```
