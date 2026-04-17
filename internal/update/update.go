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
	"regexp"
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

	// maxSignatureSize caps .sig / .pem auxiliary files. These are tiny in
	// practice — a base64 signature is ~96 bytes and a Fulcio cert is ~1-2 KB.
	maxSignatureSize = 64 << 10

	// UnsignedReleaseEnv lets callers opt into SHA-only verification for
	// releases that predate the cosign signing pipeline. When we're
	// confident every supported release is signed, flip the default and
	// this variable becomes a no-op.
	UnsignedReleaseEnv = "LEO_ALLOW_UNSIGNED_RELEASE"
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

// checksumFileName is the artifact name goreleaser emits alongside each
// release archive. It's a const because it never changes at runtime; the URL
// templates are vars so tests can swap them.
const (
	checksumFileName     = "checksums.txt"
	signatureFileName    = "checksums.txt.sig"
	certFileName         = "checksums.txt.pem"
	artifactBaseTemplate = "https://github.com/" + repoOwner + "/" + repoName + "/releases/download/%s/%s"
)

// UpdateOptions controls optional knobs on DownloadAndReplace. Zero value
// means "strict": fetch+verify signature, abort if missing.
type UpdateOptions struct {
	// AllowUnsigned downgrades signature verification to SHA-only with a
	// warning. Used during the rollout window where not every release has
	// a .sig + .pem pair yet. Wire this to a CLI flag or the
	// LEO_ALLOW_UNSIGNED_RELEASE env var.
	AllowUnsigned bool
	// Warn is called when AllowUnsigned causes a fallback. Defaults to a
	// no-op — the caller CLI supplies a real stderr writer.
	Warn func(format string, args ...any)
}

var (
	httpClient           = &http.Client{Timeout: 30 * time.Second}
	osExecutable         = os.Executable
	downloadURLTemplate  = artifactBaseTemplate
	checksumURLTemplate  = "https://github.com/" + repoOwner + "/" + repoName + "/releases/download/%s/" + checksumFileName
	signatureURLTemplate = "https://github.com/" + repoOwner + "/" + repoName + "/releases/download/%s/" + signatureFileName
	certURLTemplate      = "https://github.com/" + repoOwner + "/" + repoName + "/releases/download/%s/" + certFileName

	// newSignatureVerifier is overridden in tests that want to inject a
	// fixture verifier keyed to a self-signed root. It takes the target
	// release version so the returned verifier can pin the SAN regex to
	// that exact tag (see SignatureVerifierForVersion for rationale).
	newSignatureVerifier = SignatureVerifierForVersion
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

// PackageManagerHomebrew is the manager string returned by
// PackageManagerInstall when the running binary lives inside a Homebrew
// Cellar.
const PackageManagerHomebrew = "homebrew"

// homebrewCellarPattern matches a Homebrew keg binary path:
// "<prefix>/Cellar/leo/<version>/bin/leo". Anchored to the suffix so a
// user directory that happens to contain "/Cellar/leo/" (e.g. a Go
// workspace, a fork, or test fixtures) doesn't false-positive. The
// leading prefix is free-form to cover all Homebrew roots — /opt/homebrew
// (ARM macOS), /usr/local (Intel macOS), /home/linuxbrew/.linuxbrew, and
// any custom HOMEBREW_CELLAR.
var homebrewCellarPattern = regexp.MustCompile(`/Cellar/leo/[^/]+/bin/leo$`)

// PackageManagerInstall reports whether the running binary was installed by
// a system package manager that owns its lifecycle. It returns the manager
// name (e.g. PackageManagerHomebrew) and the resolved binary path, or
// ("", "") if no package manager is detected.
func PackageManagerInstall() (manager, path string) {
	p, err := osExecutable()
	if err != nil {
		return "", ""
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", ""
	}
	if homebrewCellarPattern.MatchString(resolved) {
		return PackageManagerHomebrew, resolved
	}
	return "", ""
}

// DownloadAndReplace is the strict-verification entrypoint. Signature
// verification is mandatory; pass DownloadAndReplaceWithOptions to relax.
func DownloadAndReplace(version string) (string, error) {
	return DownloadAndReplaceWithOptions(version, UpdateOptions{})
}

// DownloadAndReplaceWithOptions downloads the release archive for the
// current platform, verifies its cosign signature, verifies its SHA-256
// against the release's checksums.txt, extracts the binary, and atomically
// replaces the running binary. Returns the path that was replaced. Any
// signature mismatch, checksum mismatch, missing checksums file, or
// missing entry aborts the update before the binary is replaced.
//
// If opts.AllowUnsigned is set, a missing signature file degrades to
// SHA-only verification with a warning — but a present-and-invalid
// signature still aborts.
func DownloadAndReplaceWithOptions(version string, opts UpdateOptions) (string, error) {
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

	checksumsBody, err := fetchChecksumsFile(version)
	if err != nil {
		return "", err
	}

	if err := verifyChecksumsSignature(version, checksumsBody, opts); err != nil {
		return "", fmt.Errorf("verifying release signature: %w", err)
	}

	expected, err := parseChecksum(string(checksumsBody), archiveName)
	if err != nil {
		return "", err
	}
	if err := verifyArchiveChecksumAgainst(archiveName, archiveBytes, expected); err != nil {
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

// verifyArchiveChecksumAgainst compares a pre-fetched expected SHA-256 (as
// lowercase hex) against the SHA-256 of archiveBytes. Split out from the
// fetch step so DownloadAndReplaceWithOptions can verify the checksums
// file's own signature before trusting any value inside it.
func verifyArchiveChecksumAgainst(archiveName string, archiveBytes []byte, expected string) error {
	sum := sha256.Sum256(archiveBytes)
	got := hex.EncodeToString(sum[:])
	if got != expected {
		return fmt.Errorf("sha256 mismatch: archive hashed to %s, %s lists %s "+
			"(retry the update; if it persists, file a bug at "+
			"https://github.com/%s/%s/issues)",
			got, checksumFileName, expected, repoOwner, repoName)
	}
	return nil
}

// fetchChecksumsFile downloads the raw checksums.txt body for a release
// without parsing it. The caller is expected to first verify the body's
// cosign signature, then parse individual entries.
func fetchChecksumsFile(version string) ([]byte, error) {
	url := fmt.Sprintf(checksumURLTemplate, version)
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", checksumFileName, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s returned %d", checksumFileName, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxChecksumsSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", checksumFileName, err)
	}
	if len(body) > maxChecksumsSize {
		return nil, fmt.Errorf("%s exceeds %d byte limit", checksumFileName, maxChecksumsSize)
	}
	return body, nil
}

// verifyChecksumsSignature fetches checksums.txt.sig + checksums.txt.pem
// from the same release and runs the embedded Fulcio-root-backed verifier.
// Returns nil if the signature is valid. If the signature files are absent
// and opts.AllowUnsigned is true, emits a warning and returns nil; a
// missing signature without AllowUnsigned is treated as a fatal error.
func verifyChecksumsSignature(version string, checksumsBody []byte, opts UpdateOptions) error {
	sig, sigPresent, err := fetchOptionalArtifact(version, signatureFileName, signatureURLTemplate)
	if err != nil {
		return err
	}
	cert, certPresent, err := fetchOptionalArtifact(version, certFileName, certURLTemplate)
	if err != nil {
		return err
	}

	if !sigPresent || !certPresent {
		if !opts.AllowUnsigned {
			return fmt.Errorf("release is missing %s or %s — refusing to update; "+
				"rerun with --allow-unsigned (or set %s=1) to fall back to SHA-only verification",
				signatureFileName, certFileName, UnsignedReleaseEnv)
		}
		if opts.Warn != nil {
			opts.Warn("WARNING: release %s has no cosign signature; relying on SHA-256 only.\n"+
				"         Signatures become mandatory in a future release.", version)
		}
		return nil
	}

	verifier, err := newSignatureVerifier(version)
	if err != nil {
		return fmt.Errorf("building verifier: %w", err)
	}

	return verifier.Verify(checksumsBody, sig, cert)
}

// fetchOptionalArtifact GETs one of the auxiliary release files. A 404 is
// not an error — it means "this release isn't signed yet" and the caller
// decides whether that's fatal. Any other HTTP error is surfaced.
func fetchOptionalArtifact(version, name, urlTemplate string) (body []byte, present bool, err error) {
	url := fmt.Sprintf(urlTemplate, version)
	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, false, fmt.Errorf("downloading %s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("downloading %s returned %d", name, resp.StatusCode)
	}

	body, err = io.ReadAll(io.LimitReader(resp.Body, maxSignatureSize+1))
	if err != nil {
		return nil, false, fmt.Errorf("reading %s: %w", name, err)
	}
	if len(body) > maxSignatureSize {
		return nil, false, fmt.Errorf("%s exceeds %d byte limit", name, maxSignatureSize)
	}
	return body, true, nil
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
		// goreleaser emits exactly two fields: "<hash>  <filename>". Reject
		// lines with other shapes so a malicious checksums.txt with three
		// fields ("hash hash filename") can't be parsed as valid by picking
		// the last field as the filename — we want a hard mismatch, not a
		// silent acceptance of the wrong hash.
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[1] == archiveName {
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
