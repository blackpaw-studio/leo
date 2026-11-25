//go:build darwin

package peercred

import "golang.org/x/sys/unix"

// peerPID reads LOCAL_PEERPID: the pid of the process that connected.
func peerPID(fd int) (int, error) {
	return unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID)
}
