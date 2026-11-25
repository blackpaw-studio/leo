//go:build linux

package peercred

import "golang.org/x/sys/unix"

// peerPID reads SO_PEERCRED: the credentials of the process that connected.
func peerPID(fd int) (int, error) {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, err
	}
	return int(cred.Pid), nil
}
