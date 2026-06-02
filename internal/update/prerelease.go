package update

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// Limits for prerelease artifact handling. Keep these in lockstep with
// the equivalents in update.go so a tampered server can't push us into
// runaway allocations on either path.
const (
	// maxArtifactZipSize caps the artifact zip we'll download from GitHub
	// Actions. A release's worth of tarballs + sigs is ~50 MB; 500 MB is a
	// generous ceiling that still trips on a runaway server.
	maxArtifactZipSize = 500 << 20

	// prereleaseArtifactName is the well-known name the prerelease
	// workflow uploads. Hardcoded by both sides so the resolver doesn't
	// need to guess.
	prereleaseArtifactName = "leo-prerelease"

	// prereleaseWorkflowFile is the filename the prerelease workflow
	// lives in. Used both to filter workflow runs and to construct the
	// cosign SAN regex.
	prereleaseWorkflowFile = "prerelease.yml"
)

// prereleaseAPIBase is the GitHub REST API root for the Leo repo. It's
// a var (not a const) so tests can swap it for an httptest server.
var prereleaseAPIBase = "https://api.github.com/repos/" + repoOwner + "/" + repoName

// prereleaseTokenSource lets tests stub the auth lookup. Production
// resolves real tokens via gh + env vars.
var prereleaseTokenSource = resolveGitHubToken

// prereleaseVerifierForPR is the cosign identity factory injected here
// so tests can stub it the same way update.go stubs newSignatureVerifier.
var prereleaseVerifierForPR = SignatureVerifierForPullRequest

// prereleaseVersionPattern matches version strings produced by the
// prerelease workflow's goreleaser snapshot template:
//
//	pr-<pr-number>-<7+ hex chars>
//
// Used by the CLI to decide whether to route a --version flag through
// the PR flow or the stable flow.
var prereleaseVersionPattern = regexp.MustCompile(`^pr-([0-9]+)-([0-9a-f]{7,40})$`)

// IsPrereleaseVersion reports whether a version string targets a PR
// build rather than a tagged release.
func IsPrereleaseVersion(version string) bool {
	return prereleaseVersionPattern.MatchString(version)
}

// ParsePrereleaseVersion extracts the PR number and short SHA from a
// "pr-<n>-<sha>" version string. Returns an error if the shape doesn't
// match.
func ParsePrereleaseVersion(version string) (prNumber int, shortSHA string, err error) {
	m := prereleaseVersionPattern.FindStringSubmatch(version)
	if m == nil {
		return 0, "", fmt.Errorf("version %q is not a prerelease tag (want pr-<n>-<sha>)", version)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, "", fmt.Errorf("parsing pr number in %q: %w", version, err)
	}
	return n, m[2], nil
}

// PrereleaseOptions controls the prerelease update flow. The zero value
// is the strict mode used in production.
type PrereleaseOptions struct {
	// Token overrides token resolution; if empty, prereleaseTokenSource
	// is consulted (gh CLI → env vars).
	Token string
	// AllowUnsigned mirrors UpdateOptions.AllowUnsigned: when set, a
	// missing sig/cert pair degrades to SHA-only with a warning instead
	// of aborting. A present-but-invalid signature still aborts.
	AllowUnsigned bool
	// Warn receives advisory messages (auth source, fallbacks). Nil is
	// a no-op.
	Warn func(format string, args ...any)
}

// DownloadAndReplacePR fetches the most-recent successful prerelease
// build for the given PR, verifies its checksum + cosign signature, and
// atomically replaces the running binary. Returns the path that was
// replaced and the version string (e.g. "pr-42-a1b2c3d") so the caller
// can report it.
func DownloadAndReplacePR(ctx context.Context, prNumber int, opts PrereleaseOptions) (string, string, error) {
	if prNumber <= 0 {
		return "", "", errors.New("pr number must be positive")
	}

	token, source, err := resolveToken(opts)
	if err != nil {
		return "", "", err
	}
	if opts.Warn != nil {
		opts.Warn("Authenticating to GitHub via %s.", source)
	}

	run, err := findLatestPassingPRRun(ctx, token, prNumber, "")
	if err != nil {
		return "", "", err
	}
	return downloadAndReplaceFromRun(ctx, token, prNumber, run, opts)
}

// DownloadAndReplacePRVersion resolves a pinned `pr-<n>-<sha>` version
// to the workflow run that produced it, then runs the same verify+install
// flow as DownloadAndReplacePR. Used by `leo update --version pr-…`.
func DownloadAndReplacePRVersion(ctx context.Context, version string, opts PrereleaseOptions) (string, string, error) {
	prNumber, shortSHA, err := ParsePrereleaseVersion(version)
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

	run, err := findLatestPassingPRRun(ctx, token, prNumber, fullSHA)
	if err != nil {
		return "", "", err
	}
	return downloadAndReplaceFromRun(ctx, token, prNumber, run, opts)
}

func resolveToken(opts PrereleaseOptions) (token, source string, err error) {
	if opts.Token != "" {
		return opts.Token, "PrereleaseOptions.Token", nil
	}
	return prereleaseTokenSource()
}

// downloadAndReplaceFromRun is shared by the --pr and --version paths.
// It downloads the artifact zip from `run`, verifies everything inside,
// extracts the platform binary, and atomically replaces the running
// binary.
func downloadAndReplaceFromRun(ctx context.Context, token string, prNumber int, run workflowRun, opts PrereleaseOptions) (string, string, error) {
	binaryPath, err := osExecutable()
	if err != nil {
		return "", "", fmt.Errorf("finding current binary: %w", err)
	}
	binaryPath, err = filepath.EvalSymlinks(binaryPath)
	if err != nil {
		return "", "", fmt.Errorf("resolving binary path: %w", err)
	}

	artifactID, err := findPrereleaseArtifact(ctx, token, run.ID)
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
	expectedPRN, _, err := ParsePrereleaseVersion(bundle.version)
	if err != nil {
		return "", "", fmt.Errorf("artifact metadata: %w", err)
	}
	if expectedPRN != prNumber {
		return "", "", fmt.Errorf("artifact metadata reports PR #%d but we requested PR #%d", expectedPRN, prNumber)
	}

	if err := verifyPrereleaseSignature(prNumber, bundle, opts); err != nil {
		return "", "", fmt.Errorf("verifying prerelease signature: %w", err)
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

// verifyPrereleaseSignature is the prerelease counterpart of
// verifyChecksumsSignature. It uses the PR-specific verifier (cosign
// identity = prerelease.yml@refs/pull/<n>/merge).
func verifyPrereleaseSignature(prNumber int, bundle artifactBundle, opts PrereleaseOptions) error {
	if len(bundle.signature) == 0 || len(bundle.certificate) == 0 {
		if !opts.AllowUnsigned {
			return fmt.Errorf("artifact is missing %s or %s — refusing to update; "+
				"rerun with --allow-unsigned to fall back to SHA-only verification",
				signatureFileName, certFileName)
		}
		if opts.Warn != nil {
			opts.Warn("WARNING: prerelease build has no cosign signature; relying on SHA-256 only.")
		}
		return nil
	}

	verifier, err := prereleaseVerifierForPR(prNumber)
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

// installBinary extracts the `leo` binary from the platform tarball and
// atomically replaces the running binary. Shared with the stable update
// flow's tail end — same temp-file + chmod + rename pattern as
// DownloadAndReplaceWithOptions.
func installBinary(binaryPath string, archive []byte) error {
	binaryDir := filepath.Dir(binaryPath)
	tmpFile, err := os.CreateTemp(binaryDir, "leo-update-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		tmpFile.Close()
		os.Remove(tmpPath)
	}()

	if err := extractBinaryFromTarGz(bytes.NewReader(archive), tmpFile); err != nil {
		return fmt.Errorf("extracting binary: %w", err)
	}
	tmpFile.Close()

	oldInfo, err := os.Stat(binaryPath)
	if err != nil {
		return fmt.Errorf("reading binary permissions: %w", err)
	}
	if err := os.Chmod(tmpPath, oldInfo.Mode()); err != nil {
		return fmt.Errorf("setting permissions: %w", err)
	}
	if err := os.Rename(tmpPath, binaryPath); err != nil {
		return fmt.Errorf("replacing binary (try running with sudo): %w", err)
	}
	return nil
}

// resolveGitHubToken returns a GitHub API token plus a short label
// describing where it came from. Resolution order:
//
//  1. LEO_GITHUB_TOKEN (leo-specific override)
//  2. GH_TOKEN (gh CLI standard)
//  3. GITHUB_TOKEN (Actions / generic)
//  4. `gh auth token` shell-out if gh is on PATH
//
// Returns a clear error pointing the user at `gh auth login` if nothing
// works.
func resolveGitHubToken() (token, source string, err error) {
	for _, env := range []string{"LEO_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if v := os.Getenv(env); v != "" {
			return v, "$" + env, nil
		}
	}
	if path, err := exec.LookPath("gh"); err == nil {
		out, runErr := exec.Command(path, "auth", "token").Output()
		if runErr == nil {
			tok := strings.TrimSpace(string(out))
			if tok != "" {
				return tok, "gh auth token", nil
			}
		}
	}
	return "", "", errors.New(
		"no GitHub credentials available — install the gh CLI and run `gh auth login`, " +
			"or set GH_TOKEN / LEO_GITHUB_TOKEN to a token with `repo` and `actions:read` scopes")
}

// workflowRun is the trimmed payload we read from
// /actions/workflows/<id>/runs.
type workflowRun struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	HeadSHA      string `json:"head_sha"`
	Event        string `json:"event"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	PullRequests []struct {
		Number int `json:"number"`
	} `json:"pull_requests"`
}

type workflowRunsResponse struct {
	WorkflowRuns []workflowRun `json:"workflow_runs"`
}

// findLatestPassingPRRun returns the most recent successful prerelease
// workflow run for the given PR. If headSHA is non-empty, the run's
// head_sha must match it (used by the --version path to pin to a
// specific commit).
func findLatestPassingPRRun(ctx context.Context, token string, prNumber int, headSHA string) (workflowRun, error) {
	// We query runs for the prerelease workflow filtered by event=pull_request
	// and status=success. GitHub returns them newest-first.
	u := fmt.Sprintf("%s/actions/workflows/%s/runs?event=pull_request&status=success&per_page=50",
		prereleaseAPIBase, prereleaseWorkflowFile)
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
		if run.Conclusion != "success" {
			continue
		}
		for _, pr := range run.PullRequests {
			if pr.Number == prNumber {
				return run, nil
			}
		}
	}

	if headSHA != "" {
		short := headSHA
		if len(short) > 7 {
			short = short[:7]
		}
		return workflowRun{}, fmt.Errorf("no successful prerelease run for PR #%d at commit %s (the build may have failed or expired)", prNumber, short)
	}
	return workflowRun{}, fmt.Errorf("no successful prerelease run found for PR #%d (has the prerelease workflow run yet?)", prNumber)
}

type artifact struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Expired    bool   `json:"expired"`
	ArchiveURL string `json:"archive_download_url"`
}

type artifactsResponse struct {
	Artifacts []artifact `json:"artifacts"`
}

func findPrereleaseArtifact(ctx context.Context, token string, runID int64) (int64, error) {
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
		if a.Name != prereleaseArtifactName {
			continue
		}
		if a.Expired {
			return 0, fmt.Errorf("artifact %q on run %d has expired (artifacts are retained for ~14 days)", a.Name, runID)
		}
		return a.ID, nil
	}
	return 0, fmt.Errorf("no %q artifact found on run %d", prereleaseArtifactName, runID)
}

func downloadArtifactZip(ctx context.Context, token string, artifactID int64) ([]byte, error) {
	u := fmt.Sprintf("%s/actions/artifacts/%d/zip", prereleaseAPIBase, artifactID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading artifact zip: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("artifact download returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxArtifactZipSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading artifact zip: %w", err)
	}
	if int64(len(body)) > maxArtifactZipSize {
		return nil, fmt.Errorf("artifact zip exceeds %d byte limit", maxArtifactZipSize)
	}
	return body, nil
}

// artifactBundle is the parsed contents of the leo-prerelease artifact
// zip. The zip mirrors goreleaser's dist/ layout.
type artifactBundle struct {
	version     string            // from metadata.json
	checksums   []byte            // checksums.txt
	signature   []byte            // checksums.txt.sig (base64-wrapped sig)
	certificate []byte            // checksums.txt.pem (Fulcio leaf)
	archives    map[string][]byte // archive filename → tar.gz body
}

type artifactMetadata struct {
	Version string `json:"version"`
}

// readArtifactBundle unzips the artifact and extracts the four file
// classes we care about. Anything we don't recognise is silently
// ignored — the artifact may contain extra goreleaser-generated files
// (config.yaml, etc.) that we don't need.
func readArtifactBundle(zipBytes []byte) (artifactBundle, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return artifactBundle{}, fmt.Errorf("opening artifact zip: %w", err)
	}

	bundle := artifactBundle{
		archives: make(map[string][]byte),
	}

	for _, f := range zr.File {
		name := filepath.Base(f.Name)
		// Hard reject path traversal entries in the zip — we read by
		// basename, so an attacker who can get a "../escape" file into
		// the zip can't actually traverse, but reject anyway so the
		// behavior is obvious to future readers.
		if strings.Contains(f.Name, "..") {
			continue
		}

		switch {
		case name == "checksums.txt":
			data, err := readZipEntry(f, maxChecksumsSize)
			if err != nil {
				return artifactBundle{}, err
			}
			bundle.checksums = data
		case name == "checksums.txt.sig":
			data, err := readZipEntry(f, maxSignatureSize)
			if err != nil {
				return artifactBundle{}, err
			}
			bundle.signature = data
		case name == "checksums.txt.pem":
			data, err := readZipEntry(f, maxSignatureSize)
			if err != nil {
				return artifactBundle{}, err
			}
			bundle.certificate = data
		case name == "metadata.json":
			data, err := readZipEntry(f, maxSignatureSize)
			if err != nil {
				return artifactBundle{}, err
			}
			var md artifactMetadata
			if err := json.Unmarshal(data, &md); err != nil {
				return artifactBundle{}, fmt.Errorf("decoding metadata.json: %w", err)
			}
			bundle.version = md.Version
		case strings.HasPrefix(name, "leo_") && strings.HasSuffix(name, ".tar.gz"):
			data, err := readZipEntry(f, maxArchiveSize)
			if err != nil {
				return artifactBundle{}, err
			}
			bundle.archives[name] = data
		}
	}

	if len(bundle.checksums) == 0 {
		return artifactBundle{}, errors.New("artifact bundle is missing checksums.txt")
	}
	if len(bundle.archives) == 0 {
		return artifactBundle{}, errors.New("artifact bundle is missing platform archives")
	}
	return bundle, nil
}

func readZipEntry(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("opening %s in zip: %w", f.Name, err)
	}
	defer rc.Close()
	body, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s from zip: %w", f.Name, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s exceeds %d byte limit", f.Name, limit)
	}
	return body, nil
}

type commitResponse struct {
	SHA string `json:"sha"`
}

// resolveCommitSHA expands a short SHA to its full 40-char form. The
// GitHub commits endpoint accepts abbreviated SHAs directly.
func resolveCommitSHA(ctx context.Context, token, shortSHA string) (string, error) {
	u := fmt.Sprintf("%s/commits/%s", prereleaseAPIBase, url.PathEscape(shortSHA))
	body, err := githubAPIGet(ctx, token, u)
	if err != nil {
		return "", err
	}
	var resp commitResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("decoding commit: %w", err)
	}
	if resp.SHA == "" {
		return "", errors.New("commit response did not include a sha")
	}
	return resp.SHA, nil
}

// githubAPIGet is a thin wrapper around httpClient that sets the
// standard GitHub API headers + bearer auth. Used for all read-only
// JSON endpoints.
func githubAPIGet(ctx context.Context, token, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling %s: %w", u, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxArtifactZipSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// Trim the body so we don't dump megabytes of HTML on a 5xx.
		snippet := string(body)
		if len(snippet) > 200 {
			snippet = snippet[:200] + "…"
		}
		return nil, fmt.Errorf("GET %s returned %d: %s", u, resp.StatusCode, snippet)
	}
	return body, nil
}
