// Package bridgemod embeds the leo-bridge Claude Code mod and writes it to
// disk so claude agents can load it with --plugin-dir.
package bridgemod

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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

// modFS holds only what Claude Code loads: the manifest and the hooks
// directory. Tests and the typings Claude Code generates are left out.
//
//go:embed leo-bridge/.claude-plugin/plugin.json leo-bridge/hooks
var modFS embed.FS

// Materialize writes the embedded mod to <stateDir>/mods/leo-bridge/<version>/
// and returns that directory.
//
// A version directory is immutable once written: when it already exists it
// is returned untouched, so a running claude never sees its mod change under
// it. The mod is written to a temp directory beside the target and renamed
// into place, so a concurrent caller or a crash never leaves a partial mod at
// the target path.
func Materialize(stateDir, version string) (string, error) {
	if stateDir == "" || !filepath.IsAbs(stateDir) {
		return "", fmt.Errorf("bridgemod: state dir must be an absolute path, got %q", stateDir)
	}
	v, err := sanitizeVersion(version)
	if err != nil {
		return "", err
	}
	parent := filepath.Join(stateDir, "mods", Name)
	target := filepath.Join(parent, v)

	done, err := installed(target)
	if err != nil {
		return "", err
	}
	if done {
		return target, nil
	}
	if err := os.MkdirAll(parent, dirPerm); err != nil {
		return "", fmt.Errorf("bridgemod: create %s: %w", parent, err)
	}
	tmp, err := os.MkdirTemp(parent, ".tmp-"+v+"-")
	if err != nil {
		return "", fmt.Errorf("bridgemod: create temp dir: %w", err)
	}
	defer os.RemoveAll(tmp) // no-op once renamed into place

	if err := writeMod(tmp); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, target); err != nil {
		// Another caller may have won the race; its copy is identical.
		if done, statErr := installed(target); done && statErr == nil {
			return target, nil
		}
		return "", fmt.Errorf("bridgemod: install %s: %w", target, err)
	}
	return target, nil
}

// installed reports whether target already holds a materialized mod.
// A missing target is (false, nil); anything at the path that is not a
// directory (a file, a symlink) is an error rather than something to replace.
func installed(target string) (bool, error) {
	info, err := os.Lstat(target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("bridgemod: inspect %s: %w", target, err)
	case !info.IsDir():
		return false, fmt.Errorf("bridgemod: %s exists and is not a directory", target)
	}
	return true, nil
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
		if err := os.WriteFile(dest, data, filePerm); err != nil {
			return fmt.Errorf("bridgemod: write %s: %w", dest, err)
		}
		return nil
	})
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
