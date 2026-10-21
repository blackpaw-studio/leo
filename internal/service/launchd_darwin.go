//go:build darwin

package service

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// bootstrapAttempts and bootstrapBackoff bound launchctl bootstrap retries:
// a bootstrap right after a bootout can race launchd's own teardown.
const (
	bootstrapAttempts = 3
	bootstrapBackoff  = 500 * time.Millisecond
)

// DaemonLabel is the launchd label leo's own CLI install uses for home.
func DaemonLabel(home string) string { return daemonLabel(home) }

// PlistPath is where leo's CLI install writes the LaunchAgent plist for home.
func PlistPath(home string) string { return plistPath(home) }

// bootstrapWithRetry loads the plist at path, retrying with a linear backoff.
// sleep is injected so tests never wait.
func bootstrapWithRetry(path string, sleep func(time.Duration)) error {
	var err error
	for i := 0; i < bootstrapAttempts; i++ {
		if _, err = runCommand("launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), path); err == nil {
			return nil
		}
		sleep(time.Duration(i+1) * bootstrapBackoff)
	}
	return fmt.Errorf("launchctl bootstrap: %w", err)
}

// Bootstrap loads the plist at path into the user's gui domain.
func Bootstrap(path string) error { return bootstrapWithRetry(path, time.Sleep) }

// RestartLabel force-restarts the launchd job named label. Unlike
// RestartDaemon it never looks for a leo-owned plist: the job belongs to
// whoever registered it (Leo.app), so a failure is launchd's alone.
func RestartLabel(label string) error {
	if out, err := runCommand("launchctl", "kickstart", "-k", launchctlTarget(label)); err != nil {
		return fmt.Errorf("launchctl kickstart %s: %w: %s", label, err, strings.TrimSpace(out))
	}
	return nil
}

// LabelLoaded reports whether launchd has a job registered under label.
func LabelLoaded(label string) bool {
	_, err := runCommand("launchctl", "print", launchctlTarget(label))
	return err == nil
}

// DaemonStatusLabel describes the launchd job named label.
func DaemonStatusLabel(label string) (string, error) {
	out, err := runCommand("launchctl", "print", launchctlTarget(label))
	if err != nil {
		return "not registered", nil
	}
	for _, line := range strings.Split(out, "\n") {
		if pid, ok := strings.CutPrefix(strings.TrimSpace(line), "pid = "); ok {
			return fmt.Sprintf("running (pid %s)", pid), nil
		}
	}
	return "installed but not running", nil
}

// BootoutLabel unloads the launchd job named label.
func BootoutLabel(label string) error {
	if out, err := bootout(label); err != nil {
		return fmt.Errorf("launchctl bootout %s: %w: %s", label, err, strings.TrimSpace(out))
	}
	return nil
}

// plutilNoKey is plutil's message for a key path that does not exist.
const plutilNoKey = "No value at that key path"

// PlistEnv returns the plist's EnvironmentVariables, or an empty map when the
// plist has none.
func PlistEnv(plist string) (map[string]string, error) {
	out, err := runCommand("plutil", "-extract", "EnvironmentVariables", "json", "-o", "-", plist)
	if err != nil {
		if strings.Contains(out, plutilNoKey) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("reading EnvironmentVariables from %s: %w: %s", plist, err, strings.TrimSpace(out))
	}
	env := map[string]string{}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		return nil, fmt.Errorf("parsing EnvironmentVariables from %s: %w", plist, err)
	}
	return env, nil
}

// PlistProgramPath returns the plist's ProgramArguments[0].
func PlistProgramPath(plist string) (string, error) {
	out, err := runCommand("plutil", "-extract", "ProgramArguments.0", "raw", "-o", "-", plist)
	if err != nil {
		return "", fmt.Errorf("reading ProgramArguments from %s: %w: %s", plist, err, strings.TrimSpace(out))
	}
	return strings.TrimSpace(out), nil
}

// DaemonRegistered reports whether launchd has leo's own job for home loaded.
func DaemonRegistered(home string) bool { return LabelLoaded(daemonLabel(home)) }
