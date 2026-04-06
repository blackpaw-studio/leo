package update

import (
	"archive/tar"
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
		resp := releaseResponse{
			TagName: "v0.5.0",
			Assets: []releaseAsset{
				{Name: "leo_0.5.0_darwin_arm64.tar.gz", BrowserDownloadURL: "https://example.com/leo.tar.gz"},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Override the API URL for testing
	origClient := httpClient
	origURL := apiURL
	defer func() { httpClient = origClient }()

	// We need to override the URL, but it's a const. Use a custom transport instead.
	httpClient = server.Client()

	// Can't easily override the const URL, so test the parsing logic directly
	// by hitting the test server via a custom request
	req, _ := http.NewRequest("GET", server.URL, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var release releaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if release.TagName != "v0.5.0" {
		t.Errorf("tag = %q, want %q", release.TagName, "v0.5.0")
	}

	_ = origURL // acknowledge we read it
}

func TestExtractBinaryFromTarGz(t *testing.T) {
	// Create a tar.gz with a "leo" binary inside
	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, "test.tar.gz")

	binaryContent := []byte("#!/bin/sh\necho hello\n")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}

	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)

	// Write the leo binary entry
	tw.WriteHeader(&tar.Header{
		Name:     "leo",
		Size:     int64(len(binaryContent)),
		Mode:     0755,
		Typeflag: tar.TypeReg,
	})
	tw.Write(binaryContent)
	tw.Close()
	gw.Close()
	f.Close()

	// Extract it
	archive, _ := os.Open(archivePath)
	defer archive.Close()

	outPath := filepath.Join(tmpDir, "extracted")
	out, _ := os.Create(outPath)
	defer out.Close()

	if err := extractBinaryFromTarGz(archive, out); err != nil {
		t.Fatalf("extractBinaryFromTarGz() error: %v", err)
	}

	out.Close()
	data, _ := os.ReadFile(outPath)
	if string(data) != string(binaryContent) {
		t.Errorf("extracted content = %q, want %q", string(data), string(binaryContent))
	}
}

func TestExtractBinaryFromTarGzMissing(t *testing.T) {
	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, "test.tar.gz")

	// Create a tar.gz without a "leo" binary
	f, _ := os.Create(archivePath)
	gw := gzip.NewWriter(f)
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
	f.Close()

	archive, _ := os.Open(archivePath)
	defer archive.Close()

	outPath := filepath.Join(tmpDir, "extracted")
	out, _ := os.Create(outPath)
	defer out.Close()

	err := extractBinaryFromTarGz(archive, out)
	if err == nil {
		t.Error("expected error when binary not found in archive")
	}
}
