package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIsNewer(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		want    bool
	}{
		{"v0.3.0", "v0.4.0", true},
		{"v0.3.0", "v0.3.1", true},
		{"v0.3.0", "v0.3.0", false},
		{"v0.4.0", "v0.3.0", false},
		{"0.3.0", "0.4.0", true},
		{"v1.0.0", "v0.9.9", false},
		{"v0.9.9", "v1.0.0", true},
		{"dev", "v0.1.0", true},
		{"", "v0.1.0", true},
		{"v0.3.0-20-g8e5070e-dirty", "v0.3.0", false},
		{"v0.3.0-20-g8e5070e-dirty", "v0.4.0", true},
		// major version bump
		{"v1.2.3", "v2.0.0", true},
		{"v2.0.0", "v1.99.99", false},
		// patch-only bump
		{"v1.0.0", "v1.0.1", true},
		{"v1.0.1", "v1.0.0", false},
		// minor-only bump
		{"v1.0.0", "v1.1.0", true},
		{"v1.1.0", "v1.0.0", false},
		// same dirty version vs newer
		{"v0.3.0-dirty", "v0.3.0", false},
		// both dev/empty
		{"dev", "dev", true},
		{"", "", true},
		// latest is empty (no release found edge case)
		{"v1.0.0", "", false},
		// no v prefix on either
		{"1.0.0", "2.0.0", true},
		// mixed v prefix
		{"v1.0.0", "2.0.0", true},
		{"1.0.0", "v2.0.0", true},
	}

	for _, tt := range tests {
		t.Run(tt.current+"_vs_"+tt.latest, func(t *testing.T) {
			got := IsNewer(tt.current, tt.latest)
			if got != tt.want {
				t.Errorf("IsNewer(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		input string
		want  [3]int
	}{
		{"0.3.0", [3]int{0, 3, 0}},
		{"1.2.3", [3]int{1, 2, 3}},
		{"0.3.0-20-g8e5070e-dirty", [3]int{0, 3, 0}},
		{"10.20.30", [3]int{10, 20, 30}},
		{"1", [3]int{1, 0, 0}},
		{"1.2", [3]int{1, 2, 0}},
		// empty string
		{"", [3]int{0, 0, 0}},
		// non-numeric parts default to 0
		{"abc.def.ghi", [3]int{0, 0, 0}},
		// pre-release suffix stripped
		{"2.1.0-rc1", [3]int{2, 1, 0}},
		// extra dots: SplitN keeps "3.4" as third element, Atoi("3.4") → 0
		{"1.2.3.4", [3]int{1, 2, 0}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseVersion(tt.input)
			if got != tt.want {
				t.Errorf("parseVersion(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestCheckLatestVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(releaseResponse{
			TagName: "v0.5.0",
			Assets: []releaseAsset{
				{Name: "leo_0.5.0_darwin_arm64.tar.gz", BrowserDownloadURL: "https://example.com/leo.tar.gz"},
			},
		})
	}))
	defer server.Close()

	origURL := apiURL
	defer func() { apiURL = origURL }()
	apiURL = server.URL

	version, err := CheckLatestVersion()
	if err != nil {
		t.Fatalf("CheckLatestVersion() error: %v", err)
	}
	if version != "v0.5.0" {
		t.Errorf("version = %q, want %q", version, "v0.5.0")
	}
}

func TestCheckLatestVersionAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	origURL := apiURL
	defer func() { apiURL = origURL }()
	apiURL = server.URL

	_, err := CheckLatestVersion()
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestCheckLatestVersionEmptyTag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(releaseResponse{TagName: ""})
	}))
	defer server.Close()

	origURL := apiURL
	defer func() { apiURL = origURL }()
	apiURL = server.URL

	_, err := CheckLatestVersion()
	if err == nil {
		t.Error("expected error for empty tag")
	}
}

func TestCheckLatestVersionBadJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer server.Close()

	origURL := apiURL
	defer func() { apiURL = origURL }()
	apiURL = server.URL

	_, err := CheckLatestVersion()
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// buildTestArchive creates a tar.gz containing a "leo" binary with the given content.
func buildTestArchive(t *testing.T, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	tw.WriteHeader(&tar.Header{
		Name:     "leo",
		Size:     int64(len(content)),
		Mode:     0755,
		Typeflag: tar.TypeReg,
	})
	tw.Write(content)
	tw.Close()
	gw.Close()
	return buf.Bytes()
}

// sha256Hex returns the lowercase hex-encoded SHA-256 of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// testServer wires up a fake GitHub release CDN that serves the supplied
// archive under archiveName and a checksums.txt body. It returns the server
// plus a teardown that restores the package-level URL templates.
func testServer(t *testing.T, archiveName string, archive []byte, checksumsBody string) (*httptest.Server, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/"+checksumFileName):
			if checksumsBody == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write([]byte(checksumsBody))
		case strings.HasSuffix(r.URL.Path, "/"+archiveName):
			w.Write(archive)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	origDL := downloadURLTemplate
	origCS := checksumURLTemplate
	downloadURLTemplate = server.URL + "/%s/%s"
	checksumURLTemplate = server.URL + "/%s/" + checksumFileName

	return server, func() {
		server.Close()
		downloadURLTemplate = origDL
		checksumURLTemplate = origCS
	}
}

func TestDownloadAndReplace(t *testing.T) {
	binaryContent := []byte("#!/bin/sh\necho updated\n")
	archive := buildTestArchive(t, binaryContent)
	archiveName := fmt.Sprintf("leo_0.5.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	checksums := fmt.Sprintf("%s  %s\n", sha256Hex(archive), archiveName)

	_, teardown := testServer(t, archiveName, archive, checksums)
	defer teardown()

	tmpDir := t.TempDir()
	fakeBinary := filepath.Join(tmpDir, "leo")
	os.WriteFile(fakeBinary, []byte("old binary"), 0750)

	origExec := osExecutable
	defer func() { osExecutable = origExec }()
	osExecutable = func() (string, error) { return fakeBinary, nil }

	path, err := DownloadAndReplace("v0.5.0")
	if err != nil {
		t.Fatalf("DownloadAndReplace() error: %v", err)
	}

	resolvedFake, _ := filepath.EvalSymlinks(fakeBinary)
	if path != resolvedFake {
		t.Errorf("replaced path = %q, want %q", path, resolvedFake)
	}

	data, _ := os.ReadFile(fakeBinary)
	if string(data) != string(binaryContent) {
		t.Errorf("binary content = %q, want %q", string(data), string(binaryContent))
	}
}

func TestDownloadAndReplaceHTTPError(t *testing.T) {
	// Archive endpoint returns 404 — checksums don't matter here because
	// download fails first.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	fakeBinary := filepath.Join(tmpDir, "leo")
	os.WriteFile(fakeBinary, []byte("old"), 0750)

	origExec := osExecutable
	defer func() { osExecutable = origExec }()
	osExecutable = func() (string, error) { return fakeBinary, nil }

	origDL := downloadURLTemplate
	origCS := checksumURLTemplate
	defer func() {
		downloadURLTemplate = origDL
		checksumURLTemplate = origCS
	}()
	downloadURLTemplate = server.URL + "/%s/%s"
	checksumURLTemplate = server.URL + "/%s/checksums.txt"

	_, err := DownloadAndReplace("v0.5.0")
	if err == nil {
		t.Error("expected error for 404 response")
	}
}

func TestDownloadAndReplaceExecError(t *testing.T) {
	origExec := osExecutable
	defer func() { osExecutable = origExec }()
	osExecutable = func() (string, error) { return "", fmt.Errorf("no executable") }

	_, err := DownloadAndReplace("v0.5.0")
	if err == nil {
		t.Error("expected error when os.Executable fails")
	}
}

func TestDownloadAndReplaceChecksumMismatch(t *testing.T) {
	binaryContent := []byte("real binary")
	archive := buildTestArchive(t, binaryContent)
	archiveName := fmt.Sprintf("leo_0.5.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)

	// Publish a checksum that doesn't match the archive we actually serve.
	checksums := fmt.Sprintf("%s  %s\n", strings.Repeat("0", 64), archiveName)

	_, teardown := testServer(t, archiveName, archive, checksums)
	defer teardown()

	tmpDir := t.TempDir()
	fakeBinary := filepath.Join(tmpDir, "leo")
	os.WriteFile(fakeBinary, []byte("original"), 0750)

	origExec := osExecutable
	defer func() { osExecutable = origExec }()
	osExecutable = func() (string, error) { return fakeBinary, nil }

	_, err := DownloadAndReplace("v0.5.0")
	if err == nil {
		t.Fatal("expected checksum mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Errorf("error = %q, want mention of sha256 mismatch", err.Error())
	}

	// Binary on disk must not have been replaced.
	data, _ := os.ReadFile(fakeBinary)
	if string(data) != "original" {
		t.Errorf("binary was replaced despite checksum mismatch: %q", string(data))
	}
}

func TestDownloadAndReplaceMissingChecksumsFile(t *testing.T) {
	binaryContent := []byte("binary")
	archive := buildTestArchive(t, binaryContent)
	archiveName := fmt.Sprintf("leo_0.5.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)

	// Empty checksumsBody → handler returns 404 for the checksums path.
	_, teardown := testServer(t, archiveName, archive, "")
	defer teardown()

	tmpDir := t.TempDir()
	fakeBinary := filepath.Join(tmpDir, "leo")
	os.WriteFile(fakeBinary, []byte("original"), 0750)

	origExec := osExecutable
	defer func() { osExecutable = origExec }()
	osExecutable = func() (string, error) { return fakeBinary, nil }

	_, err := DownloadAndReplace("v0.5.0")
	if err == nil {
		t.Fatal("expected error for missing checksums.txt, got nil")
	}
	if !strings.Contains(err.Error(), checksumFileName) {
		t.Errorf("error = %q, want mention of %s", err.Error(), checksumFileName)
	}

	data, _ := os.ReadFile(fakeBinary)
	if string(data) != "original" {
		t.Errorf("binary was replaced despite missing checksums: %q", string(data))
	}
}

func TestDownloadAndReplaceMissingArchiveEntry(t *testing.T) {
	binaryContent := []byte("binary")
	archive := buildTestArchive(t, binaryContent)
	archiveName := fmt.Sprintf("leo_0.5.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)

	// checksums.txt exists but doesn't mention our archive.
	checksums := fmt.Sprintf("%s  some-other-file.tar.gz\n", sha256Hex(archive))

	_, teardown := testServer(t, archiveName, archive, checksums)
	defer teardown()

	tmpDir := t.TempDir()
	fakeBinary := filepath.Join(tmpDir, "leo")
	os.WriteFile(fakeBinary, []byte("original"), 0750)

	origExec := osExecutable
	defer func() { osExecutable = origExec }()
	osExecutable = func() (string, error) { return fakeBinary, nil }

	_, err := DownloadAndReplace("v0.5.0")
	if err == nil {
		t.Fatal("expected error for missing archive entry, got nil")
	}
	if !strings.Contains(err.Error(), "no entry for") {
		t.Errorf("error = %q, want mention of missing entry", err.Error())
	}
}

func TestDownloadAndReplaceValidChecksumCorruptArchive(t *testing.T) {
	garbage := []byte("this is not a tar.gz")
	archiveName := fmt.Sprintf("leo_0.5.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	checksums := fmt.Sprintf("%s  %s\n", sha256Hex(garbage), archiveName)

	_, teardown := testServer(t, archiveName, garbage, checksums)
	defer teardown()

	tmpDir := t.TempDir()
	fakeBinary := filepath.Join(tmpDir, "leo")
	os.WriteFile(fakeBinary, []byte("original"), 0750)

	origExec := osExecutable
	defer func() { osExecutable = origExec }()
	osExecutable = func() (string, error) { return fakeBinary, nil }

	_, err := DownloadAndReplace("v0.5.0")
	if err == nil {
		t.Fatal("expected extraction error for non-tarball payload, got nil")
	}
	if !strings.Contains(err.Error(), "extracting binary") {
		t.Errorf("error = %q, want mention of extraction failure", err.Error())
	}

	data, _ := os.ReadFile(fakeBinary)
	if string(data) != "original" {
		t.Errorf("binary was replaced despite extraction failure: %q", string(data))
	}
}

func TestDownloadAndReplaceOversizedArchive(t *testing.T) {
	// Handler ignores the path and always streams oversize bytes, so we
	// don't need to compute archiveName for the route.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 64<<10)
		remaining := maxArchiveSize + 2
		for remaining > 0 {
			n := len(chunk)
			if n > remaining {
				n = remaining
			}
			if _, err := w.Write(chunk[:n]); err != nil {
				return
			}
			remaining -= n
		}
	}))
	defer server.Close()

	origDL := downloadURLTemplate
	origCS := checksumURLTemplate
	defer func() {
		downloadURLTemplate = origDL
		checksumURLTemplate = origCS
	}()
	downloadURLTemplate = server.URL + "/%s/%s"
	checksumURLTemplate = server.URL + "/%s/" + checksumFileName

	tmpDir := t.TempDir()
	fakeBinary := filepath.Join(tmpDir, "leo")
	os.WriteFile(fakeBinary, []byte("original"), 0750)

	origExec := osExecutable
	defer func() { osExecutable = origExec }()
	osExecutable = func() (string, error) { return fakeBinary, nil }

	_, err := DownloadAndReplace("v0.5.0")
	if err == nil {
		t.Fatal("expected size-limit error, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error = %q, want mention of size limit", err.Error())
	}

	data, _ := os.ReadFile(fakeBinary)
	if string(data) != "original" {
		t.Errorf("binary was replaced despite oversize rejection: %q", string(data))
	}
}

func TestParseChecksum(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		archiveName string
		want        string
		wantErr     bool
	}{
		{
			name:        "two-space goreleaser format",
			body:        "abc123  leo_0.5.0_darwin_arm64.tar.gz\n",
			archiveName: "leo_0.5.0_darwin_arm64.tar.gz",
			want:        "abc123",
		},
		{
			name:        "tab separator",
			body:        "abc123\tleo_0.5.0_darwin_arm64.tar.gz\n",
			archiveName: "leo_0.5.0_darwin_arm64.tar.gz",
			want:        "abc123",
		},
		{
			name: "multiple entries",
			body: "" +
				"111  leo_0.5.0_linux_amd64.tar.gz\n" +
				"222  leo_0.5.0_darwin_arm64.tar.gz\n" +
				"333  leo_0.5.0_linux_arm64.tar.gz\n",
			archiveName: "leo_0.5.0_darwin_arm64.tar.gz",
			want:        "222",
		},
		{
			name:        "uppercase hex is lowercased",
			body:        "ABCDEF  leo_0.5.0_darwin_arm64.tar.gz\n",
			archiveName: "leo_0.5.0_darwin_arm64.tar.gz",
			want:        "abcdef",
		},
		{
			name:        "skips blank lines and comments",
			body:        "\n# generated by goreleaser\n\nabc  leo_0.5.0_darwin_arm64.tar.gz\n",
			archiveName: "leo_0.5.0_darwin_arm64.tar.gz",
			want:        "abc",
		},
		{
			name:        "missing entry",
			body:        "abc  other.tar.gz\n",
			archiveName: "leo_0.5.0_darwin_arm64.tar.gz",
			wantErr:     true,
		},
		{
			name:        "empty body",
			body:        "",
			archiveName: "leo_0.5.0_darwin_arm64.tar.gz",
			wantErr:     true,
		},
		{
			// A malicious checksums.txt with three fields could have tricked
			// a "last field wins" parser into treating the second hash as
			// the filename. parseChecksum rejects non-two-field lines so
			// this shape is ignored entirely and the archive looks missing.
			name:        "three-field line is ignored",
			body:        "aaa  bbb  leo_0.5.0_darwin_arm64.tar.gz\n",
			archiveName: "leo_0.5.0_darwin_arm64.tar.gz",
			wantErr:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseChecksum(tc.body, tc.archiveName)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtractBinaryFromTarGz(t *testing.T) {
	binaryContent := []byte("#!/bin/sh\necho hello\n")
	archive := buildTestArchive(t, binaryContent)

	var out bytes.Buffer
	if err := extractBinaryFromTarGz(bytes.NewReader(archive), &out); err != nil {
		t.Fatalf("extractBinaryFromTarGz() error: %v", err)
	}

	if out.String() != string(binaryContent) {
		t.Errorf("extracted content = %q, want %q", out.String(), string(binaryContent))
	}
}

func TestExtractBinaryFromTarGzNestedPath(t *testing.T) {
	binaryContent := []byte("nested binary")
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	tw.WriteHeader(&tar.Header{
		Name:     "leo_0.5.0_darwin_arm64/leo",
		Size:     int64(len(binaryContent)),
		Mode:     0755,
		Typeflag: tar.TypeReg,
	})
	tw.Write(binaryContent)
	tw.Close()
	gw.Close()

	var out bytes.Buffer
	if err := extractBinaryFromTarGz(bytes.NewReader(buf.Bytes()), &out); err != nil {
		t.Fatalf("extractBinaryFromTarGz() error: %v", err)
	}
	if out.String() != string(binaryContent) {
		t.Errorf("extracted = %q, want %q", out.String(), string(binaryContent))
	}
}

func TestExtractBinaryFromTarGzInvalidGzip(t *testing.T) {
	var out bytes.Buffer
	err := extractBinaryFromTarGz(bytes.NewReader([]byte("not gzip data")), &out)
	if err == nil {
		t.Error("expected error for invalid gzip data")
	}
}

func TestExtractBinaryFromTarGzMissing(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	tw.WriteHeader(&tar.Header{
		Name:     "README.md",
		Size:     5,
		Mode:     0644,
		Typeflag: tar.TypeReg,
	})
	tw.Write([]byte("hello"))
	tw.Close()
	gw.Close()

	var out bytes.Buffer
	err := extractBinaryFromTarGz(bytes.NewReader(buf.Bytes()), &out)
	if err == nil {
		t.Error("expected error when binary not found in archive")
	}
}

func TestPackageManagerInstall(t *testing.T) {
	// Build a fake Cellar layout and a few non-Cellar layouts, all rooted in
	// t.TempDir() so filepath.EvalSymlinks succeeds (it requires real paths).
	root := t.TempDir()

	makeBinary := func(rel string) string {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write: %v", err)
		}
		return full
	}

	tests := []struct {
		name        string
		binPath     string
		execErr     error
		wantManager string
	}{
		{
			name:        "arm homebrew cellar",
			binPath:     makeBinary("opt/homebrew/Cellar/leo/0.1.0/bin/leo"),
			wantManager: PackageManagerHomebrew,
		},
		{
			name:        "intel homebrew cellar",
			binPath:     makeBinary("usr/local/Cellar/leo/0.1.0/bin/leo"),
			wantManager: PackageManagerHomebrew,
		},
		{
			name:        "linuxbrew cellar",
			binPath:     makeBinary("home/linuxbrew/.linuxbrew/Cellar/leo/0.1.0/bin/leo"),
			wantManager: PackageManagerHomebrew,
		},
		{
			name:        "go install path",
			binPath:     makeBinary("home/user/go/bin/leo"),
			wantManager: "",
		},
		{
			name:        "system path",
			binPath:     makeBinary("usr/local/bin/leo"),
			wantManager: "",
		},
		{
			name:        "osExecutable returns error",
			execErr:     fmt.Errorf("no executable"),
			wantManager: "",
		},
	}

	origExec := osExecutable
	defer func() { osExecutable = origExec }()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.execErr != nil {
				osExecutable = func() (string, error) { return "", tc.execErr }
			} else {
				path := tc.binPath
				osExecutable = func() (string, error) { return path, nil }
			}

			gotMgr, gotPath := PackageManagerInstall()
			if gotMgr != tc.wantManager {
				t.Errorf("manager = %q, want %q", gotMgr, tc.wantManager)
			}
			if tc.wantManager == "" && gotPath != "" {
				t.Errorf("path = %q, want empty", gotPath)
			}
			if tc.wantManager != "" && gotPath == "" {
				t.Error("path is empty, want non-empty resolved path")
			}
		})
	}
}
