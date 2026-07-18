package update

import (
	"fmt"
	"os/exec"
	"strings"
)

// appleTeamID is Blackpaw Studio's Apple Developer Team ID. Darwin release
// binaries starting with v0.10.0 are signed under this identity and
// notarized by Apple.
const appleTeamID = "52M9C6892K"

// appleCodesignRequirement is a codesign "designated requirement" string
// pinning the check to our specific Developer ID Application certificate
// (issued by Apple's own root, not just "any Apple-signed binary").
var appleCodesignRequirement = fmt.Sprintf(
	`anchor apple generic and certificate leaf[subject.OU] = %q`, appleTeamID,
)

// runCodesign execs the real /usr/bin/codesign binary. Overridden in tests
// so unit tests never invoke real codesign (and pass on non-macOS CI).
var runCodesign = func(requirement, path string) ([]byte, error) {
	cmd := exec.Command("/usr/bin/codesign", "--verify", "--strict", "-R="+requirement, "--", path)
	return cmd.CombinedOutput()
}

// verifyAppleSignature runs `codesign --verify --strict` against path,
// requiring a valid Apple Developer ID signature issued to our team. Returns
// nil if the signature checks out; otherwise wraps codesign's output for
// diagnostics.
func verifyAppleSignature(path string) error {
	out, err := runCodesign(appleCodesignRequirement, path)
	if err != nil {
		return fmt.Errorf("codesign verification failed: %w: %s", err, string(out))
	}
	return nil
}

// shouldVerifyAppleSignature reports whether the Apple codesign check
// applies for the given OS and target release version. Factored out of
// DownloadAndReplaceWithOptions so the decision is testable on any platform.
func shouldVerifyAppleSignature(goos, version string) bool {
	return goos == "darwin" && appleSignatureExpected(version)
}

// appleSignatureExpected reports whether version is expected to carry an
// Apple codesign signature — true for v0.10.0 and later, the first signed
// release. "dev" and "" (unversioned/local builds) are never expected to be
// signed.
func appleSignatureExpected(version string) bool {
	trimmed := strings.TrimPrefix(version, "v")
	if trimmed == "dev" || trimmed == "" {
		return false
	}

	const (
		firstSignedMajor = 0
		firstSignedMinor = 10
		firstSignedPatch = 0
	)

	parts := parseVersion(trimmed)
	if parts[0] != firstSignedMajor {
		return parts[0] > firstSignedMajor
	}
	if parts[1] != firstSignedMinor {
		return parts[1] > firstSignedMinor
	}
	return parts[2] >= firstSignedPatch
}
