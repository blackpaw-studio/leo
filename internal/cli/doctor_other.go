//go:build !darwin

package cli

// checkLocalNetwork is a no-op on non-macOS platforms: the Local Network
// privacy consent prompt is a macOS-only concept.
func checkLocalNetwork(_ string, _ bool) LocalNetworkStatus {
	return LocalNetworkStatus{
		State:  "n/a",
		Detail: "macOS only",
	}
}
