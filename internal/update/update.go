package update

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
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

	// maxArchiveSize caps the tarball we're willing to download into memory.
	// Real release archives are ~10 MB; 100 MB is a generous ceiling.
	maxArchiveSize = 100 << 20

	// maxChecksumsSize caps the checksums.txt file. It should be a few hundred
	// bytes at most.
	maxChecksumsSize = 1 << 20
)

var apiURL = "https://api.github.com/repos/" + repoOwner + "/" + repoName + "/releases/latest"

type releaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type releaseResponse struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

var (
	httpClient           = &http.Client{Timeout: 30 * time.Second}
	osExecutable         = os.Executable
	downloadURLTemplate  = "https://github.com/" + repoOwner + "/" + repoName + "/releases/download/%s/%s"
	checksumURLTemplate  = "https://github.com/" + repoOwner + "/" + repoName + "/releases/download/%s/checksums.txt"
	checksumFileName     = "checksums.txt"
)

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
// verifies its SHA-256 against the release's checksums.txt, extracts the
// binary, and atomically replaces the running binary. Returns the path that
// was replaced. Any checksum mismatch, missing checksums file, or missing
// entry aborts the update before the binary is replaced.
func DownloadAndReplace(version string) (string, error) {
	binaryPath, err := osExecutable()
	if err != nil {
		return "", fmt.Errorf("finding current binary: %w", err)
	}
	binaryPath, err = filepath.EvalSymlinks(binaryPath)
	if err != nil {
		return "", fmt.Errorf("resolving binary path: %w", err)
	}

	versionNum := strings.TrimPrefix(version, "v")
	archiveName := fmt.Sprintf("leo_%s_%s_%s.tar.gz", versionNum, runtime.GOOS, runtime.GOARCH)

	archiveBytes, err := downloadArchive(version, archiveName)
	if err != nil {
		return "", err
	}

	if err := verifyArchiveChecksum(version, archiveName, archiveBytes); err != nil {
		return "", fmt.Errorf("verifying %s: %w", archiveName, err)
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

	if err := extractBinaryFromTarGz(bytes.NewReader(archiveBytes), tmpFile); err != nil {
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

// downloadArchive fetches the release tarball into memory, capped at
// maxArchiveSize.
func downloadArchive(version, archiveName string) ([]byte, error) {
	url := fmt.Sprintf(downloadURLTemplate, version, archiveName)
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", archiveName, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned %d for %s", resp.StatusCode, archiveName)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxArchiveSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", archiveName, err)
	}
	if len(body) > maxArchiveSize {
		return nil, fmt.Errorf("archive %s exceeds %d byte limit", archiveName, maxArchiveSize)
	}
	return body, nil
}

// verifyArchiveChecksum fetches checksums.txt for the release and compares
// its entry for archiveName against the SHA-256 of archiveBytes. Returns an
// error if the checksums file can't be fetched, the archive isn't listed,
// or the hash doesn't match.
func verifyArchiveChecksum(version, archiveName string, archiveBytes []byte) error {
	expected, err := fetchExpectedChecksum(version, archiveName)
	if err != nil {
		return err
	}

	sum := sha256.Sum256(archiveBytes)
	got := hex.EncodeToString(sum[:])
	if got != expected {
		return fmt.Errorf("sha256 mismatch: archive hashed to %s, checksums.txt lists %s", got, expected)
	}
	return nil
}

// fetchExpectedChecksum downloads checksums.txt for the given release and
// returns the expected SHA-256 (as lowercase hex) for archiveName.
func fetchExpectedChecksum(version, archiveName string) (string, error) {
	url := fmt.Sprintf(checksumURLTemplate, version)
	resp, err := httpClient.Get(url)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", checksumFileName, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s returned %d", checksumFileName, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxChecksumsSize+1))
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", checksumFileName, err)
	}
	if len(body) > maxChecksumsSize {
		return "", fmt.Errorf("%s exceeds %d byte limit", checksumFileName, maxChecksumsSize)
	}

	return parseChecksum(string(body), archiveName)
}

// parseChecksum scans a goreleaser-style checksums.txt body for the entry
// matching archiveName and returns its SHA-256 hex. Lines look like:
//
//	<sha256-hex>  <filename>
func parseChecksum(body, archiveName string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Accept one or more spaces between hash and filename; goreleaser emits two.
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[len(fields)-1] == archiveName {
			return strings.ToLower(fields[0]), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading %s: %w", checksumFileName, err)
	}
	return "", fmt.Errorf("no entry for %s in %s", archiveName, checksumFileName)
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
