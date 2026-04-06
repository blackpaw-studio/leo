package update

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	repoOwner = "blackpaw-studio"
	repoName  = "leo"
	apiURL    = "https://api.github.com/repos/" + repoOwner + "/" + repoName + "/releases/latest"
)

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type releaseResponse struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// CheckLatestVersion returns the latest release tag from GitHub (e.g. "v0.5.2").
func CheckLatestVersion() (string, error) {
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var release releaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("decoding release: %w", err)
	}

	if release.TagName == "" {
		return "", fmt.Errorf("no tag found in latest release")
	}

	return release.TagName, nil
}

// IsNewer returns true if latest is a newer version than current.
// Handles "dev" as always older. Both versions may have a "v" prefix.
func IsNewer(current, latest string) bool {
	current = strings.TrimPrefix(current, "v")
	latest = strings.TrimPrefix(latest, "v")

	if current == "dev" || current == "" {
		return true
	}

	currentParts := parseVersion(current)
	latestParts := parseVersion(latest)

	for i := 0; i < 3; i++ {
		if latestParts[i] > currentParts[i] {
			return true
		}
		if latestParts[i] < currentParts[i] {
			return false
		}
	}
	return false
}

func parseVersion(v string) [3]int {
	// Strip anything after a hyphen (e.g. "0.3.0-20-g8e5070e-dirty")
	if idx := strings.Index(v, "-"); idx >= 0 {
		v = v[:idx]
	}

	var parts [3]int
	for i, s := range strings.SplitN(v, ".", 3) {
		if i >= 3 {
			break
		}
		n, _ := strconv.Atoi(s)
		parts[i] = n
	}
	return parts
}

// DownloadAndReplace downloads the release archive for the current platform,
// extracts the binary, and atomically replaces the running binary.
// Returns the path that was replaced.
func DownloadAndReplace(version string) (string, error) {
	binaryPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("finding current binary: %w", err)
	}
	binaryPath, err = filepath.EvalSymlinks(binaryPath)
	if err != nil {
		return "", fmt.Errorf("resolving binary path: %w", err)
	}

	versionNum := strings.TrimPrefix(version, "v")
	archiveName := fmt.Sprintf("leo_%s_%s_%s.tar.gz", versionNum, runtime.GOOS, runtime.GOARCH)
	downloadURL := fmt.Sprintf("https://github.com/%s/%s/releases/download/%s/%s",
		repoOwner, repoName, version, archiveName)

	resp, err := httpClient.Get(downloadURL)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", archiveName, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned %d for %s", resp.StatusCode, archiveName)
	}

	// Extract the leo binary from the tar.gz into a temp file in the same directory
	binaryDir := filepath.Dir(binaryPath)
	tmpFile, err := os.CreateTemp(binaryDir, "leo-update-*")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		tmpFile.Close()
		os.Remove(tmpPath) // cleanup on failure; no-op if renamed
	}()

	if err := extractBinaryFromTarGz(resp.Body, tmpFile); err != nil {
		return "", fmt.Errorf("extracting binary: %w", err)
	}
	tmpFile.Close()

	// Copy permissions from old binary
	oldInfo, err := os.Stat(binaryPath)
	if err != nil {
		return "", fmt.Errorf("reading binary permissions: %w", err)
	}
	if err := os.Chmod(tmpPath, oldInfo.Mode()); err != nil {
		return "", fmt.Errorf("setting permissions: %w", err)
	}

	// Atomic replace
	if err := os.Rename(tmpPath, binaryPath); err != nil {
		return "", fmt.Errorf("replacing binary (try running with sudo): %w", err)
	}

	return binaryPath, nil
}

func extractBinaryFromTarGz(r io.Reader, w io.Writer) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("opening gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("binary 'leo' not found in archive")
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}

		if filepath.Base(hdr.Name) == "leo" && hdr.Typeflag == tar.TypeReg {
			// Limit extraction to 500MB to prevent decompression bombs
			const maxBinarySize = 500 << 20
			if _, err := io.Copy(w, io.LimitReader(tr, maxBinarySize)); err != nil {
				return fmt.Errorf("writing binary: %w", err)
			}
			return nil
		}
	}
}
