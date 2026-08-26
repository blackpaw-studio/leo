package service

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
)

// homeIdentityHashLen is the number of hex characters kept from the
// sha256 digest of a resolved home path. 12 hex chars (48 bits) is far
// more than enough to avoid collisions between the handful of leo homes
// that could plausibly coexist on one machine, while staying short
// enough to be a readable suffix on a launchd label or systemd unit name.
const homeIdentityHashLen = 12

// resolveHomePath normalizes home into a form that is stable across
// equivalent spellings — relative vs. absolute, trailing slashes, and
// symlinks (e.g. /Users/evan vs a symlinked /home/evan) — so the same
// leo home always hashes to the same identity regardless of how it was
// invoked. EvalSymlinks requires the path to exist; when it doesn't yet
// (e.g. first install, before the home directory has been created) we
// fall back to the absolute form, which is still stable for a given
// invocation context.
func resolveHomePath(home string) string {
	path := home
	if abs, err := filepath.Abs(home); err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// homeIdentityHash returns a short, stable, filesystem/label-safe hash of
// the resolved home path. Used to scope OS-service identities (launchd
// label, systemd unit name) to a specific leo home, so a second checkout
// or a test daemon running against a different LEO_HOME never collides
// with — and can never bootout/replace — the production registration.
func homeIdentityHash(home string) string {
	sum := sha256.Sum256([]byte(resolveHomePath(home)))
	return hex.EncodeToString(sum[:])[:homeIdentityHashLen]
}

// isDefaultHome reports whether home resolves to the same path as
// defaultHome (the platform's canonical ~/.leo, as determined by the
// caller). Both are normalized through resolveHomePath so spelling
// differences (symlinks, relative paths, trailing slashes) can't split
// one logical install into two identities.
func isDefaultHome(home, defaultHome string) bool {
	if defaultHome == "" {
		return false
	}
	return resolveHomePath(home) == resolveHomePath(defaultHome)
}
