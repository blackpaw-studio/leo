package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// maxSymlinkDepth bounds symlink chasing for a dangling leo.yaml link.
const maxSymlinkDepth = 16

// writeTemp is a testability seam for the temp-file write in writeFileAtomic.
var writeTemp = writeSynced

// writeFileAtomic replaces path with data (mode 0600) so a concurrent reader
// or a crash never observes a truncated file: the content goes to a temp file
// in the same directory, is fsynced, renamed over the target, and the
// directory is fsynced to persist the rename. A failure at any step leaves
// the original untouched and removes the temp file.
func writeFileAtomic(path string, data []byte) (err error) {
	target, err := resolveWriteTarget(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(target)

	tmp, err := os.CreateTemp(dir, ".leo.yaml.*")
	if err != nil {
		return fmt.Errorf("creating temp config: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()

	if err = tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting temp config permissions: %w", err)
	}
	if err := writeTemp(tmp, data); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), target); err != nil {
		return fmt.Errorf("installing config: %w", err)
	}
	return syncDir(dir)
}

// resolveWriteTarget returns the file a write to path must replace. A
// regular or missing path is its own target. A symlink resolves to its final
// target so the rename replaces the target and the link stays a link (users
// symlink leo.yaml from a dotfiles repo); a dangling link resolves to the
// path it points at, which is then created.
func resolveWriteTarget(path string) (string, error) {
	cur := path
	for range maxSymlinkDepth {
		fi, err := os.Lstat(cur)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return cur, nil
			}
			return "", fmt.Errorf("inspecting config path: %w", err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			return cur, nil
		}
		next, err := os.Readlink(cur)
		if err != nil {
			return "", fmt.Errorf("reading config symlink: %w", err)
		}
		if !filepath.IsAbs(next) {
			next = filepath.Join(filepath.Dir(cur), next)
		}
		cur = next
	}
	return "", fmt.Errorf("config path %q: too many levels of symbolic links", path)
}

// syncDir fsyncs dir so a completed rename survives a crash. Filesystems
// that cannot fsync a directory (EINVAL) are not an error: the rename already
// happened.
func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // directory of the config being saved
	if err != nil {
		return fmt.Errorf("opening config directory: %w", err)
	}
	serr := d.Sync()
	if cerr := d.Close(); serr == nil {
		serr = cerr
	}
	if serr != nil && !errors.Is(serr, syscall.EINVAL) {
		return fmt.Errorf("syncing config directory: %w", serr)
	}
	return nil
}
