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

// Ownership seams: statOwner reads the existing target's uid/gid, tempOwner
// the temp file's actual owner (it may already inherit the directory's gid),
// chownTemp hands the temp file to the previous owner.
var (
	statOwner = fileOwner
	tempOwner = func(f *os.File) (uid, gid int, ok bool) {
		fi, err := f.Stat()
		if err != nil {
			return 0, 0, false
		}
		return fileOwner(fi)
	}
	chownTemp = func(f *os.File, uid, gid int) error { return f.Chown(uid, gid) }
)

// fileOwner returns the uid/gid of fi, or ok=false if the platform doesn't
// expose them.
func fileOwner(fi os.FileInfo) (uid, gid int, ok bool) {
	st, isStat := fi.Sys().(*syscall.Stat_t)
	if !isStat {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}

// writeFileAtomicWith replaces path with data so a concurrent reader
// or a crash never observes a truncated file: the content goes to a temp file
// in the same directory, is fsynced, renamed over the target, and the
// directory is fsynced to persist the rename. A failure at any step leaves
// the original untouched and removes the temp file.
//
// The mode is 0600, or with keepMode the existing target's permission bits
// (a shared templates file in a repo checkout is usually 0644); a new file is
// 0600 either way.
func writeFileAtomicWith(path string, data []byte, keepMode bool) (err error) {
	target, err := resolveWriteTarget(path)
	if err != nil {
		return err
	}
	mode := os.FileMode(0600)
	if keepMode {
		if fi, statErr := os.Stat(target); statErr == nil {
			mode = fi.Mode().Perm()
		}
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

	if err = tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("setting temp config permissions: %w", err)
	}
	if err = preserveOwner(tmp, target); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := writeTemp(tmp, data); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), target); err != nil {
		return fmt.Errorf("installing config: %w", err)
	}
	return syncDir(dir)
}

// preserveOwner gives tmp the owner of the existing target, because rename
// installs a new inode owned by the caller (`sudo leo config ...` would
// otherwise leave a user's leo.yaml root-owned and unreadable by the daemon).
// A differing uid needs a privileged chown, and its failure fails the save
// rather than silently changing the owner. A differing gid alone is
// best-effort: an unprivileged user may not be in the old group (EPERM), and
// the save must still succeed. A missing target has no owner to keep.
func preserveOwner(tmp *os.File, target string) error {
	fi, err := os.Stat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspecting config file: %w", err)
	}
	uid, gid, ok := statOwner(fi)
	if !ok {
		return nil
	}
	tmpUID, tmpGID, ok := tempOwner(tmp)
	if !ok || (uid == tmpUID && gid == tmpGID) {
		return nil
	}
	err = chownTemp(tmp, uid, gid)
	if err == nil || (uid == tmpUID && errors.Is(err, syscall.EPERM)) {
		return nil
	}
	return fmt.Errorf("preserving config owner %d:%d: %w", uid, gid, err)
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
