// Package bridgemod embeds the leo-bridge Claude Code mod and writes it to
// disk so claude agents can load it with --plugin-dir.
package bridgemod

import (
	"crypto/sha256"
	"embed"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Name is the mod's plugin name, as its manifest declares it.
const Name = "leo-bridge"

// root is the embedded mod's top directory inside modFS.
const root = "leo-bridge"

// maxVersionLen caps the version component of the materialized path.
const maxVersionLen = 128

const (
	dirPerm  = 0o750
	filePerm = 0o644
)

// completeMarker is written into a mod dir last, after everything else is on
// disk; a dir without it is a half-written install, not a mod.
const completeMarker = ".leo-bridge-complete"

// staleTempAge is how old a leftover temp dir must be before it is swept; an
// install takes milliseconds, so anything this old belongs to a dead process.
const staleTempAge = time.Hour

// modFS holds only what Claude Code loads: the manifest and the hooks
// directory. Tests and the typings Claude Code generates are left out.
//
//go:embed leo-bridge/.claude-plugin/plugin.json leo-bridge/hooks
var modFS embed.FS

// Materialize writes the embedded mod to
// <stateDir>/mods/leo-bridge/<version>-<hash12>/ and returns that directory,
// where hash12 is the first 12 hex chars of a SHA-256 over the embedded files.
//
// A complete version directory is immutable: when it already exists it is
// returned untouched, so a running claude never sees its mod change under it.
// The mod is written and fsynced in a temp directory beside the target, then
// marked complete (completeMarker, also fsynced) and renamed into place, so a
// concurrent caller or a crash never leaves a partial mod at the target path.
// A target without the marker (a crash after the rename, before the data hit
// the disk) is removed and rewritten. Temp dirs older than staleTempAge are
// swept on every call.
//
// Calls within one process are serialized; across processes the rename keeps
// installs safe, and only the rare repair of a broken dir can race.
func Materialize(stateDir, version string) (string, error) {
	if stateDir == "" || !filepath.IsAbs(stateDir) {
		return "", fmt.Errorf("bridgemod: state dir must be an absolute path, got %q", stateDir)
	}
	v, err := sanitizeVersion(version)
	if err != nil {
		return "", err
	}
	hash, err := contentHash()
	if err != nil {
		return "", err
	}
	// The content hash keeps a reused version string (dev, -dirty builds)
	// from resolving to a stale copy of a changed mod.
	parent := filepath.Join(stateDir, "mods", Name)
	target := filepath.Join(parent, v+"-"+hash)

	installMu.Lock()
	defer installMu.Unlock()
	sweepStaleTemps(parent, time.Now())
	return target, ensureInstalled(parent, target, v)
}

// installMu serializes Materialize within the process, so one caller's repair
// of a broken dir cannot remove another caller's fresh install.
var installMu sync.Mutex

// ensureInstalled leaves a complete target alone, removes a broken one, and
// installs a fresh copy when none is complete.
func ensureInstalled(parent, target, version string) error {
	state, err := inspect(target)
	if err != nil {
		return err
	}
	switch state {
	case dirComplete:
		return nil
	case dirBroken:
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("bridgemod: remove incomplete %s: %w", target, err)
		}
	}
	return install(parent, target, version)
}

// install writes a complete mod to a temp dir and renames it to target.
func install(parent, target, version string) error {
	if err := os.MkdirAll(parent, dirPerm); err != nil {
		return fmt.Errorf("bridgemod: create %s: %w", parent, err)
	}
	tmp, err := os.MkdirTemp(parent, tempPrefix+version+"-")
	if err != nil {
		return fmt.Errorf("bridgemod: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmp) // no-op once renamed into place

	if err := writeMod(tmp); err != nil {
		return err
	}
	if err := writeFileSynced(filepath.Join(tmp, completeMarker), nil); err != nil {
		return err
	}
	if err := syncDir(tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		// Another process may have won the race; its copy is identical.
		if state, statErr := inspect(target); state == dirComplete && statErr == nil {
			return nil
		}
		return fmt.Errorf("bridgemod: install %s: %w", target, err)
	}
	return syncDir(parent)
}

// tempPrefix starts every in-progress install's dir name.
const tempPrefix = ".tmp-"

// sweepStaleTemps removes temp dirs under parent last modified before
// now-staleTempAge. It is best effort: a sweep failure never fails a launch.
func sweepStaleTemps(parent string, now time.Time) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	cutoff := now.Add(-staleTempAge)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), tempPrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(parent, e.Name()))
	}
}

// hashLen is how many hex characters of the content hash name a mod dir.
const hashLen = 12

// contentHash is the embedded mod's hash, computed once per process.
var contentHash = sync.OnceValues(func() (string, error) {
	return hashTree(modFS, root)
})

// hashTree returns the first hashLen hex chars of a SHA-256 over every file
// under dir, in sorted path order. Each file contributes its path relative to
// dir, a NUL, its length as 8 big-endian bytes, then its content, so no two
// different trees share an encoding.
func hashTree(fsys fs.FS, dir string) (string, error) {
	var files []string
	err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("bridgemod: list embedded mod: %w", err)
	}
	sort.Strings(files)
	h := sha256.New()
	for _, p := range files {
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return "", fmt.Errorf("bridgemod: read embedded %s: %w", p, err)
		}
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(data)))
		h.Write([]byte(strings.TrimPrefix(p, dir+"/")))
		h.Write([]byte{0})
		h.Write(size[:])
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:hashLen], nil
}

// dirState is what inspect found at a mod's target path.
type dirState int

const (
	dirAbsent   dirState = iota // nothing there
	dirBroken                   // a directory without the completion marker
	dirComplete                 // a fully written mod
)

// inspect reports what target holds. Anything at the path that is not a
// directory (a file, a symlink) is an error rather than something to replace.
func inspect(target string) (dirState, error) {
	info, err := os.Lstat(target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return dirAbsent, nil
	case err != nil:
		return dirAbsent, fmt.Errorf("bridgemod: inspect %s: %w", target, err)
	case !info.IsDir():
		return dirAbsent, fmt.Errorf("bridgemod: %s exists and is not a directory", target)
	}
	marker, err := os.Lstat(filepath.Join(target, completeMarker))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return dirBroken, nil
	case err != nil:
		return dirAbsent, fmt.Errorf("bridgemod: inspect %s: %w", target, err)
	case !marker.Mode().IsRegular():
		return dirBroken, nil
	}
	return dirComplete, nil
}

// writeMod copies the embedded mod into dir, which must already exist.
func writeMod(dir string) error {
	if err := os.Chmod(dir, dirPerm); err != nil {
		return fmt.Errorf("bridgemod: chmod %s: %w", dir, err)
	}
	return fs.WalkDir(modFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
		if rel == "" {
			return nil
		}
		dest := filepath.Join(dir, filepath.FromSlash(rel))
		if d.IsDir() {
			if err := os.Mkdir(dest, dirPerm); err != nil {
				return fmt.Errorf("bridgemod: create %s: %w", dest, err)
			}
			return nil
		}
		data, err := fs.ReadFile(modFS, p)
		if err != nil {
			return fmt.Errorf("bridgemod: read embedded %s: %w", p, err)
		}
		if err := writeFileSynced(dest, data); err != nil {
			return err
		}
		// The file's own dir entry must be durable before the marker is.
		return syncDir(filepath.Dir(dest))
	})
}

// writeFileSynced creates path with data and flushes it to disk.
func writeFileSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return fmt.Errorf("bridgemod: create %s: %w", path, err)
	}
	_, writeErr := f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("bridgemod: write %s: %w", path, err)
	}
	return nil
}

// syncDir flushes a directory's entries to disk.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("bridgemod: open %s: %w", dir, err)
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return fmt.Errorf("bridgemod: sync %s: %w", dir, err)
	}
	return nil
}

// sanitizeVersion turns a build version into a single safe path component:
// letters, digits, '.', '_', '-' and '+' are kept, anything else becomes '_',
// a leading '.' becomes '_' (no ".", "..", or hidden dirs), and the result is
// capped at maxVersionLen bytes.
func sanitizeVersion(version string) (string, error) {
	trimmed := strings.TrimSpace(version)
	if trimmed == "" {
		return "", errors.New("bridgemod: version must not be empty")
	}
	safe := strings.Map(func(r rune) rune {
		if isVersionRune(r) {
			return r
		}
		return '_'
	}, trimmed)
	if strings.HasPrefix(safe, ".") {
		safe = "_" + safe[1:]
	}
	if len(safe) > maxVersionLen {
		safe = safe[:maxVersionLen]
	}
	return safe, nil
}

func isVersionRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '.', r == '_', r == '-', r == '+':
		return true
	}
	return false
}
