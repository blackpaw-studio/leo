package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIsPrereleaseVersion(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"pr-42-a1b2c3d", true},
		{"pr-1-aaaaaaa", true},
		{"pr-9999-0123456789abcdef0123456789abcdef01234567", true},
		{"v0.5.0", false},
		{"v0.5.0-rc1", false},
		{"", false},
		{"pr--a1b2c3d", false},
		{"pr-42-XYZ1234", false}, // not hex
		{"pr-42-abc", false},     // too short
		{"PR-42-a1b2c3d", false}, // case sensitive
	}
	for _, tt := range tests {
		got := IsPrereleaseVersion(tt.in)
		if got != tt.want {
			t.Errorf("IsPrereleaseVersion(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParsePrereleaseVersion(t *testing.T) {
	prNumber, sha, err := ParsePrereleaseVersion("pr-42-a1b2c3d")
	if err != nil {
		t.Fatalf("ParsePrereleaseVersion: %v", err)
	}
	if prNumber != 42 {
		t.Errorf("pr number = %d, want 42", prNumber)
	}
	if sha != "a1b2c3d" {
		t.Errorf("sha = %q, want a1b2c3d", sha)
	}

	if _, _, err := ParsePrereleaseVersion("v0.5.0"); err == nil {
		t.Error("expected error for stable tag, got nil")
	}
}

func TestSignatureVerifierForPullRequest_PinsIdentity(t *testing.T) {
	v, err := SignatureVerifierForPullRequest(42)
	if err != nil {
		t.Fatalf("SignatureVerifierForPullRequest: %v", err)
	}
	matchCases := []struct {
		san  string
		want bool
	}{
		{"https://github.com/blackpaw-studio/leo/.github/workflows/prerelease.yml@refs/pull/42/merge", true},
		{"https://github.com/blackpaw-studio/leo/.github/workflows/prerelease.yml@refs/pull/43/merge", false},
		{"https://github.com/blackpaw-studio/leo/.github/workflows/release.yml@refs/tags/v0.5.0", false},
		{"https://github.com/attacker/evil/.github/workflows/prerelease.yml@refs/pull/42/merge", false},
		{"https://github.com/blackpaw-studio/leo/.github/workflows/prerelease.yml@refs/heads/main", false},
	}
	for _, tt := range matchCases {
		got := v.SANRegex.MatchString(tt.san)
		if got != tt.want {
			t.Errorf("SAN %q match = %v, want %v", tt.san, got, tt.want)
		}
	}
}

func TestSignatureVerifierForPullRequest_RejectsNonPositive(t *testing.T) {
	if _, err := SignatureVerifierForPullRequest(0); err == nil {
		t.Error("expected error for pr=0, got nil")
	}
	if _, err := SignatureVerifierForPullRequest(-1); err == nil {
		t.Error("expected error for pr=-1, got nil")
	}
}

func TestResolveGitHubToken_PrefersLeoEnv(t *testing.T) {
	t.Setenv("LEO_GITHUB_TOKEN", "leo-token")
	t.Setenv("GH_TOKEN", "gh-token")
	t.Setenv("GITHUB_TOKEN", "github-token")

	tok, source, err := resolveGitHubToken()
	if err != nil {
		t.Fatalf("resolveGitHubToken: %v", err)
	}
	if tok != "leo-token" {
		t.Errorf("token = %q, want leo-token", tok)
	}
	if source != "$LEO_GITHUB_TOKEN" {
		t.Errorf("source = %q, want $LEO_GITHUB_TOKEN", source)
	}
}

func TestResolveGitHubToken_FallsBackThroughEnvOrder(t *testing.T) {
	t.Setenv("LEO_GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "gh-token")
	t.Setenv("GITHUB_TOKEN", "github-token")

	tok, source, err := resolveGitHubToken()
	if err != nil {
		t.Fatalf("resolveGitHubToken: %v", err)
	}
	if tok != "gh-token" {
		t.Errorf("token = %q, want gh-token", tok)
	}
	if source != "$GH_TOKEN" {
		t.Errorf("source = %q, want $GH_TOKEN", source)
	}
}

func TestResolveGitHubToken_NoCredentialsReturnsHelpfulError(t *testing.T) {
	t.Setenv("LEO_GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	// Shadow PATH so the gh binary lookup fails even if gh is installed.
	t.Setenv("PATH", "")

	_, _, err := resolveGitHubToken()
	if err == nil {
		t.Fatal("expected error with no credentials available")
	}
	if !strings.Contains(err.Error(), "gh auth login") {
		t.Errorf("error should point at gh auth login; got %q", err)
	}
}

// fakeGitHubAPI spins up an httptest server that mimics the subset of the
// GitHub REST API our resolver hits. Tests drive it by registering route
// handlers.
type fakeGitHubAPI struct {
	t       *testing.T
	mux     *http.ServeMux
	server  *httptest.Server
	prevAPI string
}

func newFakeGitHubAPI(t *testing.T) *fakeGitHubAPI {
	t.Helper()
	f := &fakeGitHubAPI{t: t, mux: http.NewServeMux()}
	f.server = httptest.NewServer(f.mux)
	f.prevAPI = prereleaseAPIBase
	prereleaseAPIBase = f.server.URL
	t.Cleanup(func() {
		prereleaseAPIBase = f.prevAPI
		f.server.Close()
	})
	return f
}

func (f *fakeGitHubAPI) handle(path string, fn http.HandlerFunc) {
	f.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			f.t.Errorf("missing or wrong auth header on %s: %q", r.URL.Path, got)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		fn(w, r)
	})
}

func TestFindLatestPassingPRRun_PicksFirstMatchingPR(t *testing.T) {
	api := newFakeGitHubAPI(t)
	api.handle("/actions/workflows/prerelease.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		// Sanity-check the query string.
		q := r.URL.Query()
		if q.Get("event") != "pull_request" {
			t.Errorf("event = %q, want pull_request", q.Get("event"))
		}
		if q.Get("status") != "success" {
			t.Errorf("status = %q, want success", q.Get("status"))
		}
		_ = json.NewEncoder(w).Encode(workflowRunsResponse{
			WorkflowRuns: []workflowRun{
				{ID: 1, Conclusion: "success", HeadSHA: "deadbeef0000000000000000000000000000000a", PullRequests: []struct {
					Number int `json:"number"`
				}{{Number: 99}}},
				{ID: 2, Conclusion: "success", HeadSHA: "cafebabe0000000000000000000000000000000b", PullRequests: []struct {
					Number int `json:"number"`
				}{{Number: 42}}},
				{ID: 3, Conclusion: "success", HeadSHA: "feedfacd0000000000000000000000000000000c", PullRequests: []struct {
					Number int `json:"number"`
				}{{Number: 42}}}, // older
			},
		})
	})

	run, err := findLatestPassingPRRun(context.Background(), "test-token", 42, "")
	if err != nil {
		t.Fatalf("findLatestPassingPRRun: %v", err)
	}
	if run.ID != 2 {
		t.Errorf("picked run %d, want 2 (newest matching)", run.ID)
	}
}

func TestFindLatestPassingPRRun_FiltersByHeadSHA(t *testing.T) {
	api := newFakeGitHubAPI(t)
	api.handle("/actions/workflows/prerelease.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		gotSHA := r.URL.Query().Get("head_sha")
		if gotSHA != "cafebabe0000000000000000000000000000000b" {
			t.Errorf("head_sha = %q, want cafebabe…", gotSHA)
		}
		_ = json.NewEncoder(w).Encode(workflowRunsResponse{
			WorkflowRuns: []workflowRun{
				{ID: 7, Conclusion: "success", HeadSHA: gotSHA, PullRequests: []struct {
					Number int `json:"number"`
				}{{Number: 42}}},
			},
		})
	})

	run, err := findLatestPassingPRRun(context.Background(), "test-token", 42, "cafebabe0000000000000000000000000000000b")
	if err != nil {
		t.Fatalf("findLatestPassingPRRun: %v", err)
	}
	if run.ID != 7 {
		t.Errorf("run.ID = %d, want 7", run.ID)
	}
}

func TestFindLatestPassingPRRun_ReturnsErrorOnNoMatch(t *testing.T) {
	api := newFakeGitHubAPI(t)
	api.handle("/actions/workflows/prerelease.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(workflowRunsResponse{
			WorkflowRuns: []workflowRun{
				{ID: 1, Conclusion: "success", PullRequests: []struct {
					Number int `json:"number"`
				}{{Number: 99}}},
			},
		})
	})

	_, err := findLatestPassingPRRun(context.Background(), "test-token", 42, "")
	if err == nil {
		t.Fatal("expected error for missing PR, got nil")
	}
	if !strings.Contains(err.Error(), "PR #42") {
		t.Errorf("error should mention PR #42; got %q", err)
	}
}

func TestFindPrereleaseArtifact_RejectsExpired(t *testing.T) {
	api := newFakeGitHubAPI(t)
	api.handle("/actions/runs/5/artifacts", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(artifactsResponse{
			Artifacts: []artifact{
				{ID: 100, Name: "leo-prerelease", Expired: true},
			},
		})
	})

	_, err := findRunArtifact(context.Background(), "test-token", 5, prereleaseArtifactName)
	if err == nil {
		t.Fatal("expected error for expired artifact")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("error should mention expired; got %q", err)
	}
}

func TestReadArtifactBundle_ParsesAllSections(t *testing.T) {
	archiveName := fmt.Sprintf("leo_pr-42-a1b2c3d_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archive := fakeBinaryTarGz(t)
	sum := sha256.Sum256(archive)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)

	zipBytes := buildArtifactZip(t, map[string][]byte{
		archiveName:           archive,
		"checksums.txt":       []byte(checksums),
		"checksums.txt.sig":   []byte("sig-bytes"),
		"checksums.txt.pem":   []byte("cert-bytes"),
		"metadata.json":       []byte(`{"version":"pr-42-a1b2c3d"}`),
		"unrelated/notes.txt": []byte("ignored"),
	})

	bundle, err := readArtifactBundle(zipBytes)
	if err != nil {
		t.Fatalf("readArtifactBundle: %v", err)
	}
	if bundle.version != "pr-42-a1b2c3d" {
		t.Errorf("version = %q, want pr-42-a1b2c3d", bundle.version)
	}
	if !bytes.Equal(bundle.checksums, []byte(checksums)) {
		t.Errorf("checksums mismatch")
	}
	if string(bundle.signature) != "sig-bytes" {
		t.Errorf("signature = %q", bundle.signature)
	}
	if string(bundle.certificate) != "cert-bytes" {
		t.Errorf("certificate = %q", bundle.certificate)
	}
	if _, ok := bundle.archives[archiveName]; !ok {
		t.Errorf("archives missing %s; have %v", archiveName, archiveKeys(bundle.archives))
	}
}

func TestReadArtifactBundle_RequiresChecksums(t *testing.T) {
	zipBytes := buildArtifactZip(t, map[string][]byte{
		"leo_pr-1-abcdefa_linux_amd64.tar.gz": []byte("fake"),
	})
	if _, err := readArtifactBundle(zipBytes); err == nil {
		t.Fatal("expected error when checksums.txt missing")
	}
}

func TestReadArtifactBundle_RequiresArchives(t *testing.T) {
	zipBytes := buildArtifactZip(t, map[string][]byte{
		"checksums.txt": []byte("# empty\n"),
	})
	if _, err := readArtifactBundle(zipBytes); err == nil {
		t.Fatal("expected error when no archives present")
	}
}

// TestDownloadAndReplacePR_EndToEnd drives the full --pr flow against a
// fake GitHub API and a stub signature verifier. It writes a fake "leo"
// binary into a tmpdir, points osExecutable at it, and verifies the file
// is replaced with the new contents.
func TestDownloadAndReplacePR_EndToEnd(t *testing.T) {
	tmpDir := t.TempDir()
	binaryPath := filepath.Join(tmpDir, "leo")
	if err := os.WriteFile(binaryPath, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	// macOS /tmp is a symlink to /private/tmp; resolve up front so the
	// assertion below matches what EvalSymlinks returns inside the code
	// under test.
	resolvedBinaryPath, err := filepath.EvalSymlinks(binaryPath)
	if err != nil {
		t.Fatal(err)
	}

	prevExec := osExecutable
	osExecutable = func() (string, error) { return binaryPath, nil }
	t.Cleanup(func() { osExecutable = prevExec })

	// End-to-end test focuses on the pipeline shape (resolve → download →
	// unzip → checksum → extract → replace). Cosign identity matching is
	// covered by TestSignatureVerifierForPullRequest_PinsIdentity and the
	// existing signature_test.go suite — those use real Fulcio fixtures.
	// Here we omit the sig/cert files and ride the AllowUnsigned path so
	// the verifier is bypassed cleanly.

	newBinary := []byte("NEW-LEO-BUILD")
	archive := tarGzWithLeo(t, newBinary)
	archiveName := fmt.Sprintf("leo_pr-42-a1b2c3d_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(archive)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)

	zipBytes := buildArtifactZip(t, map[string][]byte{
		archiveName:     archive,
		"checksums.txt": []byte(checksums),
		"metadata.json": []byte(`{"version":"pr-42-a1b2c3d"}`),
	})

	api := newFakeGitHubAPI(t)
	api.handle("/actions/workflows/prerelease.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(workflowRunsResponse{
			WorkflowRuns: []workflowRun{
				{ID: 88, Conclusion: "success", PullRequests: []struct {
					Number int `json:"number"`
				}{{Number: 42}}},
			},
		})
	})
	api.handle("/actions/runs/88/artifacts", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(artifactsResponse{
			Artifacts: []artifact{{ID: 1234, Name: "leo-prerelease"}},
		})
	})
	api.handle("/actions/artifacts/1234/zip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipBytes)
	})

	gotPath, gotVersion, err := DownloadAndReplacePR(context.Background(), 42, PrereleaseOptions{Token: "test-token", AllowUnsigned: true})
	if err != nil {
		t.Fatalf("DownloadAndReplacePR: %v", err)
	}
	if gotPath != resolvedBinaryPath {
		t.Errorf("path = %q, want %q", gotPath, resolvedBinaryPath)
	}
	if gotVersion != "pr-42-a1b2c3d" {
		t.Errorf("version = %q, want pr-42-a1b2c3d", gotVersion)
	}

	body, err := os.ReadFile(resolvedBinaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, newBinary) {
		t.Errorf("binary contents = %q, want %q", body, newBinary)
	}
}

func TestDownloadAndReplacePR_RejectsMismatchedMetadataPR(t *testing.T) {
	tmpDir := t.TempDir()
	binaryPath := filepath.Join(tmpDir, "leo")
	if err := os.WriteFile(binaryPath, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	prevExec := osExecutable
	osExecutable = func() (string, error) { return binaryPath, nil }
	t.Cleanup(func() { osExecutable = prevExec })

	archiveName := fmt.Sprintf("leo_pr-99-a1b2c3d_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archive := tarGzWithLeo(t, []byte("evil"))
	sum := sha256.Sum256(archive)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), archiveName)
	zipBytes := buildArtifactZip(t, map[string][]byte{
		archiveName:     archive,
		"checksums.txt": []byte(checksums),
		// metadata claims PR 99 but the caller asked for PR 42 — should
		// be rejected before the binary is replaced.
		"metadata.json": []byte(`{"version":"pr-99-a1b2c3d"}`),
	})

	api := newFakeGitHubAPI(t)
	api.handle("/actions/workflows/prerelease.yml/runs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(workflowRunsResponse{
			WorkflowRuns: []workflowRun{
				{ID: 88, Conclusion: "success", PullRequests: []struct {
					Number int `json:"number"`
				}{{Number: 42}}},
			},
		})
	})
	api.handle("/actions/runs/88/artifacts", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(artifactsResponse{
			Artifacts: []artifact{{ID: 1234, Name: "leo-prerelease"}},
		})
	})
	api.handle("/actions/artifacts/1234/zip", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(zipBytes)
	})

	_, _, err := DownloadAndReplacePR(context.Background(), 42, PrereleaseOptions{Token: "test-token", AllowUnsigned: true})
	if err == nil {
		t.Fatal("expected error for PR/metadata mismatch")
	}
	if !strings.Contains(err.Error(), "PR #") {
		t.Errorf("error should mention PR mismatch; got %q", err)
	}
}

// --- helpers ---

func buildArtifactZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := f.Write(body); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func tarGzWithLeo(t *testing.T, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{
		Name:     "leo",
		Mode:     0o755,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("tar write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gz close: %v", err)
	}
	return buf.Bytes()
}

func fakeBinaryTarGz(t *testing.T) []byte {
	return tarGzWithLeo(t, []byte("fake-binary"))
}

func archiveKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// silence unused-import errors when test data evolves; the package may
// not always need io / net/url, but keeping them imported is harmless.
var _ = io.Discard
var _ = url.Parse
