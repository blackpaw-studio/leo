//go:build darwin

package service

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

var (
	runCommand    = defaultRunCommand
	userHomeDirFn = os.UserHomeDir
)

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.LeoPath}}</string>
		<string>service</string>
		<string>--supervised</string>
		<string>--config</string>
		<string>{{.ConfigPath}}</string>
	</array>
	<key>WorkingDirectory</key>
	<string>{{.WorkDir}}</string>
	<key>KeepAlive</key>
	<true/>
	<key>RunAtLoad</key>
	<true/>
	<key>StandardOutPath</key>
	<string>{{.LogPath}}</string>
	<key>StandardErrorPath</key>
	<string>{{.LogPath}}</string>
{{- if .Env}}
	<key>EnvironmentVariables</key>
	<dict>
{{- range $k, $v := .Env}}
		<key>{{$k}}</key>
		<string>{{$v}}</string>
{{- end}}
	</dict>
{{- end}}
</dict>
</plist>
`

type plistData struct {
	Label      string
	LeoPath    string
	ConfigPath string
	WorkDir    string
	LogPath    string
	Env        map[string]string
}

// daemonLabelBase is the launchd label used for the default leo home
// (~/.leo). This exact string MUST NOT change: it is the identity of the
// existing production LaunchAgent registration, and changing it would
// orphan that install (launchd would treat it as a brand new, unrelated
// service).
const daemonLabelBase = "com.blackpaw.leo"

// daemonLabel derives the launchd label for the given leo home.
//
// Identity scheme: the default home (~/.leo, resolved) always maps to
// exactly daemonLabelBase, preserving backward compatibility with every
// existing production install. Any other home (a second checkout, a test
// daemon under a different LEO_HOME, etc.) gets a deterministic suffix
// derived from a hash of its resolved absolute path:
// "com.blackpaw.leo.<12 hex chars of sha256(resolved home)>". This keeps
// concurrent installs on one machine from ever bootout-ing or
// overwriting each other's registration.
func daemonLabel(home string) string {
	if isDefaultHome(home, defaultLeoHome()) {
		return daemonLabelBase
	}
	return daemonLabelBase + "." + homeIdentityHash(home)
}

// defaultLeoHome returns the platform's canonical leo home (~/.leo) using
// the same userHomeDirFn seam as plistPath, so tests can control it and
// production always resolves against the real $HOME.
func defaultLeoHome() string {
	home, err := userHomeDirFn()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".leo")
}

func plistPath(home string) string {
	userHome, _ := userHomeDirFn()
	return filepath.Join(userHome, "Library", "LaunchAgents", daemonLabel(home)+".plist")
}

// InstallDaemon writes a launchd plist and bootstraps the service.
func InstallDaemon(sc ServiceConfig) error {
	label := daemonLabel(sc.WorkDir)

	// Ensure state directory exists for log file
	if err := mkdirAll(filepath.Dir(sc.LogPath), 0750); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}

	// Render plist
	data := plistData{
		Label:      label,
		LeoPath:    sc.LeoPath,
		ConfigPath: sc.ConfigPath,
		WorkDir:    sc.WorkDir,
		LogPath:    sc.LogPath,
		Env:        sc.Env,
	}

	tmpl, err := template.New("plist").Parse(plistTemplate)
	if err != nil {
		return fmt.Errorf("parsing plist template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("rendering plist: %w", err)
	}

	path := plistPath(sc.WorkDir)
	if err := mkdirAll(filepath.Dir(path), 0750); err != nil {
		return fmt.Errorf("creating LaunchAgents directory: %w", err)
	}

	// Unload existing service if present so the upcoming bootstrap doesn't
	// conflict with a live registration. This is best-effort: a target that
	// simply wasn't loaded is not an error, but a genuine bootout failure
	// (e.g. permission denied) is surfaced as a warning rather than
	// silently discarded — launchctl gives no reliable machine-readable way
	// to tell "wasn't loaded" from "failed to unload" apart from message
	// text, so we treat the well-known "not loaded" signatures as benign
	// and warn on anything else without aborting the install.
	if err := bootout(label, path); err != nil && !isBenignBootoutError(err) {
		fmt.Fprintf(os.Stderr, "warning: launchctl bootout of %s: %v\n", label, err)
	}

	// Write the new plist to a temp file in the same directory and rename
	// it into place atomically. The plist that was previously on disk (if
	// any) is never deleted ahead of time — on any failure below, the
	// original registration's backing file stays intact rather than
	// leaving launchd with a loaded-but-missing-plist ghost.
	tmpPath := path + ".tmp"
	if err := writeFile(tmpPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("writing plist: %w", err)
	}
	if err := renameFile(tmpPath, path); err != nil {
		_ = removeFile(tmpPath)
		return fmt.Errorf("installing plist: %w", err)
	}

	uid := fmt.Sprintf("%d", os.Getuid())
	var bootstrapErr error
	for i := 0; i < 3; i++ {
		if _, bootstrapErr = runCommand("launchctl", "bootstrap", "gui/"+uid, path); bootstrapErr == nil {
			break
		}
		time.Sleep(time.Duration(i+1) * 500 * time.Millisecond)
	}
	if bootstrapErr != nil {
		return fmt.Errorf("launchctl bootstrap: %w", bootstrapErr)
	}

	return nil
}

// RemoveDaemon stops and removes the launchd service registered for home.
//
// Cleans up both halves independently: the launchd registration and the
// plist file. If either exists we attempt removal, and we only return
// "not installed" when both are already gone. This handles drift where
// one side has been cleaned up but not the other — e.g. a prior bootout
// failure that left a ghost registration, or a manually deleted plist.
func RemoveDaemon(home string) error {
	label := daemonLabel(home)
	path := plistPath(home)

	_, printErr := runCommand("launchctl", "print", launchctlTarget(label))
	loaded := printErr == nil

	_, plistStatErr := os.Stat(path)
	plistExists := plistStatErr == nil

	if !loaded && !plistExists {
		return fmt.Errorf("daemon not installed")
	}

	if loaded {
		if err := bootout(label, path); err != nil {
			return fmt.Errorf("launchctl bootout: %w", err)
		}
	}

	if plistExists {
		if err := removeFile(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removing plist: %w", err)
		}
	}

	return nil
}

// DaemonStatus returns the status of the launchd service registered for home.
//
// launchctl is the source of truth: a service can remain bootstrapped
// (and running) even after its plist file is deleted from disk, so we
// query launchctl first and only fall back to the plist check when
// launchctl has no record of the service.
func DaemonStatus(home string) (string, error) {
	label := daemonLabel(home)

	output, runErr := runCommand("launchctl", "print", launchctlTarget(label))
	if runErr == nil {
		for _, line := range strings.Split(output, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "pid = ") {
				pid := strings.TrimPrefix(trimmed, "pid = ")
				return fmt.Sprintf("running (pid %s)", pid), nil
			}
		}
		return "installed but not running", nil
	}

	if _, err := os.Stat(plistPath(home)); os.IsNotExist(err) {
		return "not installed", nil
	}

	return "installed but not running", nil
}

// RestartDaemon force-restarts the launchd service registered for home.
//
// Uses launchctl as the source of truth — a service can be running with
// its plist file missing, so we rely on the kickstart call itself to
// report whether the target is loaded. Only when kickstart fails do we
// consult the plist to produce a clearer "not installed" error.
func RestartDaemon(home string) error {
	label := daemonLabel(home)

	if _, err := runCommand("launchctl", "kickstart", "-k", launchctlTarget(label)); err != nil {
		if _, statErr := os.Stat(plistPath(home)); os.IsNotExist(statErr) {
			return fmt.Errorf("daemon not installed")
		}
		return fmt.Errorf("launchctl kickstart: %w", err)
	}

	return nil
}

// DriftDetected reports whether the launchd job for home is loaded while
// its backing plist is missing from disk — the daemon is running right
// now but launchd has nothing to re-bootstrap it from, so it will NOT
// survive the next logout/reboot. Every other state (not installed,
// installed with its plist present, or installed but not currently
// running) reports false with no detail.
func DriftDetected(home string) (bool, string, error) {
	label := daemonLabel(home)
	path := plistPath(home)

	_, printErr := runCommand("launchctl", "print", launchctlTarget(label))
	loaded := printErr == nil

	_, statErr := os.Stat(path)
	plistExists := statErr == nil

	if loaded && !plistExists {
		return true, fmt.Sprintf("launchd job %q is loaded but its plist is missing from %s — it will not survive the next logout/reboot", label, path), nil
	}
	return false, "", nil
}

// isBenignBootoutError reports whether err is launchctl's well-known
// signature for "the target wasn't loaded", which is an expected,
// non-actionable outcome for InstallDaemon's pre-emptive bootout (there's
// nothing to unload on a first install). Any other error is treated as a
// real failure worth surfacing.
func isBenignBootoutError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "No such process") ||
		strings.Contains(msg, "Could not find")
}

func bootout(label, path string) error {
	_, err := runCommand("launchctl", "bootout", launchctlTarget(label))
	return err
}

// launchctlTarget builds the gui domain specifier used by launchctl
// bootout/kickstart/print for a given service label.
func launchctlTarget(label string) string {
	return fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
}

func defaultRunCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}
