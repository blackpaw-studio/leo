// Package git provides git worktree, branch, and slug helpers used by the
// ephemeral agent lifecycle. It shells out to the `git` binary via a
// replaceable ExecGit seam so tests can intercept calls.
package git

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
)

// ErrEmptySlug is returned by SlugifyBranch when sanitization yields an empty string.
var ErrEmptySlug = errors.New("slug empty after sanitization")

var (
	slugDisallowedRe = regexp.MustCompile(`[^a-z0-9._-]+`)
	slugCollapseRe   = regexp.MustCompile(`-+`)
)

// SlugifyBranch returns a filesystem-safe slug derived from a git branch name.
// The slug is lowercased, non-[a-z0-9._-] characters are replaced with '-',
// runs of '-' are collapsed, and leading/trailing '-' or '.' are trimmed.
// Returns ErrEmptySlug if the result is empty.
func SlugifyBranch(branch string) (string, error) {
	s := strings.ToLower(branch)
	s = slugDisallowedRe.ReplaceAllString(s, "-")
	s = slugCollapseRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-.")
	if s == "" {
		return "", ErrEmptySlug
	}
	return s, nil
}

// BoundedSlug truncates slug to maxLen characters, appending a short hash of
// the full slug when truncation occurs so distinct inputs yield distinct
// outputs. Returns slug unchanged if it already fits, or if maxLen <= 0.
func BoundedSlug(slug string, maxLen int) string {
	if maxLen <= 0 || len(slug) <= maxLen {
		return slug
	}
	const hashLen = 7
	sum := sha256.Sum256([]byte(slug))
	hash := hex.EncodeToString(sum[:])[:hashLen]
	keep := maxLen - hashLen - 1 // one char for the '-' separator
	if keep < 1 {
		return hex.EncodeToString(sum[:])[:maxLen]
	}
	head := strings.TrimRight(slug[:keep], "-.")
	if head == "" {
		return hash[:maxLen]
	}
	return head + "-" + hash
}
