package service

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
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
	path := expandTildeHome(home)
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// expandTildeHome replaces a leading "~" or "~/" with the real user home
// (via the platform's userHomeDirFn seam, defined in daemon_darwin.go /
// daemon_linux.go). No caller passes a tilde-prefixed home today, but
// resolveHomePath is the single choke point every identity decision runs
// through, so handling it here closes off a whole class of future
// "~/.leo resolved differently than /Users/x/.leo" regressions cheaply.
func expandTildeHome(path string) string {
	home, err := userHomeDirFn()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
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
