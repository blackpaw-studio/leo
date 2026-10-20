//go:build !darwin

package service

import "errors"

// errNoLaunchd is returned by every launchd-only operation off macOS; a
// managed install (and so every caller of these) exists on darwin only.
var errNoLaunchd = errors.New("launchd is only available on macOS")

func DaemonLabel(string) string { return "" }

func PlistPath(string) string { return "" }

func Bootstrap(string) error { return errNoLaunchd }

func RestartLabel(string) error { return errNoLaunchd }

func LabelLoaded(string) bool { return false }

func DaemonStatusLabel(string) (string, error) { return "not registered", errNoLaunchd }

func BootoutLabel(string) error { return errNoLaunchd }

func PlistEnv(string) (map[string]string, error) { return nil, errNoLaunchd }

func PlistProgramPath(string) (string, error) { return "", errNoLaunchd }

// DaemonRegistered reports whether the OS service for home is installed.
func DaemonRegistered(home string) bool {
	status, _ := DaemonStatus(home)
	return status != "not installed"
}
