package web

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// stateDirMode is the permission the state directory must end up with.
// We re-chmod on every call so that a loose legacy dir gets tightened
// even when MkdirAll is a no-op.
const stateDirMode os.FileMode = 0700

// apiTokenFileMode is the permission the api.token file must have.
// We deliberately refuse to start if it is looser — silently auto-
// chmod'ing would mask a state we want the operator to notice
// (backup restore, sysadmin script, older Leo leaving 0644 around).
const apiTokenFileMode os.FileMode = 0600

// apiTokenFileName is the basename of the bearer-token file under the state directory.
const apiTokenFileName = "api.token"

// APITokenPath returns the path to the API bearer-token file under stateDir.
// State dir is typically <HomePath>/state.
func APITokenPath(stateDir string) string {
	return filepath.Join(stateDir, apiTokenFileName)
}

// EnsureAPIToken makes sure an API bearer token exists at APITokenPath(stateDir)
// and returns its contents. If the file is missing it generates a fresh token
// (32 random bytes, hex-encoded = 64 hex chars) and writes it with mode 0600.
// If the file exists its contents are returned unchanged — callers should not
// rotate silently. stateDir is created with mode 0700 if absent, and its perm
// is tightened to 0700 if an existing directory was looser.
//
// If the existing token file is not mode 0600, EnsureAPIToken refuses to start
// rather than silently auto-chmod'ing. The caller should delete the file (or
// fix its permissions) to unblock startup.
func EnsureAPIToken(stateDir string) (string, error) {
	if stateDir == "" {
		return "", fmt.Errorf("web: empty state dir")
	}
	if err := os.MkdirAll(stateDir, stateDirMode); err != nil {
		return "", fmt.Errorf("web: creating state dir %q: %w", stateDir, err)
	}
	// MkdirAll is a no-op if the directory already exists with looser perms,
	// so always chmod. If the admin has mounted or owned the dir in a way
	// that forbids chmod, log a warning but continue — we still refuse to
	// serve if the token file itself is loose, which is the real risk.
	if err := os.Chmod(stateDir, stateDirMode); err != nil {
		fmt.Fprintf(os.Stderr, "warning: web: chmod state dir %q to %o failed: %v\n", stateDir, stateDirMode, err)
	}

	path := APITokenPath(stateDir)

	// Fast path: existing token. os.Stat follows symlinks so an admin can
	// point api.token at a keyring-managed file; the target itself must
	// still be 0600.
	if data, err := os.ReadFile(path); err == nil {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return "", fmt.Errorf("web: stat api token %q: %w", path, statErr)
		}
		if perm := info.Mode().Perm(); perm != apiTokenFileMode {
			return "", fmt.Errorf("web: api token file %q has perm %o, expected %o; fix or delete", path, perm, apiTokenFileMode)
		}
		tok := trimToken(data)
		if tok == "" {
			return "", fmt.Errorf("web: api token file %q is empty; delete it to regenerate", path)
		}
		return tok, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("web: reading api token %q: %w", path, err)
	}

	// Generate a new token.
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("web: generating api token: %w", err)
	}
	tok := hex.EncodeToString(buf)

	// Write atomically-ish: write to a temp file, chmod, rename.
	tmp, err := os.CreateTemp(stateDir, ".api.token.*")
	if err != nil {
		return "", fmt.Errorf("web: creating temp token file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(tok + "\n"); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("web: writing temp token: %w", err)
	}
	if err := tmp.Chmod(apiTokenFileMode); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("web: chmod temp token: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("web: closing temp token: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("web: finalizing token file %q: %w", path, err)
	}
	return tok, nil
}

// trimToken strips surrounding whitespace (including trailing newline) from the
// raw file bytes, but returns a defensive copy so the caller can mutate.
func trimToken(data []byte) string {
	start, end := 0, len(data)
	for start < end {
		c := data[start]
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			break
		}
		start++
	}
	for end > start {
		c := data[end-1]
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			break
		}
		end--
	}
	return string(data[start:end])
}
