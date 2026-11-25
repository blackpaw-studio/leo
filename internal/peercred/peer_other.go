//go:build !darwin && !linux

package peercred

import "errors"

// peerPID has no lookup on this platform: every caller fails closed.
func peerPID(int) (int, error) {
	return 0, errors.New("no peer pid lookup on this platform")
}
