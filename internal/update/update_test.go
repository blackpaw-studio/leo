package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestDownloadAndReplace(t *testing.T) {
	binaryContent := []byte("#!/bin/sh\necho updated\n")
	archive := buildTestArchive(t, binaryContent)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer server.Close()

	// Create a fake binary to replace
	tmpDir := t.TempDir()
	fakeBinary := filepath.Join(tmpDir, "leo")
	os.WriteFile(fakeBinary, []byte("old binary"), 0750)

	// Override os.Executable by replacing the binary path resolution
	origExec := osExecutable
	defer func() { osExecutable = origExec }()
	osExecutable = func() (string, error) { return fakeBinary, nil }

	// Override the download URL pattern
	origURL := downloadURLTemplate
	defer func() { downloadURLTemplate = origURL }()
	downloadURLTemplate = server.URL + "/%s/%s"

	path, err := DownloadAndReplace("v0.5.0")
	if err != nil {
		t.Fatalf("DownloadAndReplace() error: %v", err)
	}

	// EvalSymlinks may resolve /var → /private/var on macOS
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

	origURL := downloadURLTemplate
	defer func() { downloadURLTemplate = origURL }()
	downloadURLTemplate = server.URL + "/%s/%s"

	_, err := DownloadAndReplace("v0.5.0")
	if err == nil {
		t.Error("expected error for 404 response")
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
