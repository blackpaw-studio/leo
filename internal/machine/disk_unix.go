//go:build darwin || linux

package machine

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// statfsDisk reports the filesystem holding path via statfs.
func statfsDisk(path string) (Disk, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return Disk{}, fmt.Errorf("statfs %s: %w", path, err)
	}
	return diskFromStatfs(path, st.Blocks, st.Bavail, uint64(st.Bsize)), nil // #nosec G115 -- kernel block size is positive
}
