//go:build darwin

package service

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

	// A non-default home whose hashed label differs from the legacy base
	// label can still collide with a live pre-scoping registration (anyone
	// who installed from this same non-default home under the old code has
	// a "com.blackpaw.leo" job pointing here already). Warn loudly and
	// leave remediation to the operator — see legacyBaseLabelCollision.
	if collided, detail := legacyBaseLabelCollision(sc.WorkDir, label); collided {
		fmt.Fprintf(os.Stderr, "warning: %s\n", detail)
	}

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
	// silently discarded.
	if out, err := bootout(label); err != nil && !isBenignBootoutError(err, out) {
		fmt.Fprintf(os.Stderr, "warning: launchctl bootout of %s: %s\n", label, strings.TrimSpace(out))
	}

	// Write the new plist to a uniquely-named temp file in the same
	// directory and rename it into place atomically. A fixed ".tmp" name
	// would let two concurrent installs interleave their writes into one
	// file and rename a corrupt result into place; os.CreateTemp's
	// randomized suffix rules that out. The plist that was previously on
	// disk (if any) is never deleted ahead of time — on any failure below,
	// the original registration's backing file stays intact rather than
	// leaving launchd with a loaded-but-missing-plist ghost.
	tmpPath, err := newTempPlistPath(path)
	if err != nil {
		return fmt.Errorf("creating temp plist: %w", err)
	}
	if err := writeFile(tmpPath, buf.Bytes(), 0644); err != nil {
		_ = removeFile(tmpPath)
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

// newTempPlistPath creates a uniquely-named, empty temp file alongside
// path (same directory, so the later rename stays on one filesystem) and
// returns its name. The caller is responsible for writing content to it
// and either renaming it into place or removing it.
func newTempPlistPath(path string) (string, error) {
	f, err := createTempFile(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return "", err
	}
	name := f.Name()
	_ = f.Close()
	return name, nil
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
		if _, err := bootout(label); err != nil {
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
func DriftDetected(home string) (bool, string) {
	label := daemonLabel(home)
	path := plistPath(home)

	_, printErr := runCommand("launchctl", "print", launchctlTarget(label))
	loaded := printErr == nil

	_, statErr := os.Stat(path)
	plistExists := statErr == nil

	if loaded && !plistExists {
		return true, fmt.Sprintf("launchd job %q is loaded but its plist is missing from %s — it will not survive the next logout/reboot", label, path)
	}
	return false, ""
}

// LegacyBaseLabelCollision reports whether a legacy com.blackpaw.leo
// launchd registration — the single shared label every install used
// before per-home label scoping — is already serving this same home from
// a non-default install path. If so, re-installing under the new hashed
// label would register a SECOND job supervising the same home, and the
// two would fight over one socket and one set of tmux sessions.
//
// Detection only: the fix is a manual `launchctl bootout`, which this
// code deliberately does not run — bootout-ing another live job during
// an unrelated install is exactly the class of surprise this whole
// change exists to prevent.
func LegacyBaseLabelCollision(home string) (bool, string) {
	return legacyBaseLabelCollision(home, daemonLabel(home))
}

func legacyBaseLabelCollision(home, computedLabel string) (bool, string) {
	if computedLabel == daemonLabelBase {
		return false, "" // this install IS the legacy label; nothing to compare against
	}

	userHome, err := userHomeDirFn()
	if err != nil {
		return false, ""
	}
	basePlistPath := filepath.Join(userHome, "Library", "LaunchAgents", daemonLabelBase+".plist")

	data, err := readFile(basePlistPath)
	if err != nil {
		return false, "" // no legacy plist on disk — nothing to warn about
	}

	workDir, ok := extractPlistWorkingDirectory(string(data))
	if !ok || resolveHomePath(workDir) != resolveHomePath(home) {
		return false, ""
	}

	return true, fmt.Sprintf(
		"a legacy launchd job %q is already registered for this leo home (%s). "+
			"Installing under %q would leave two registrations supervising the same home. "+
			"Recommended fix: launchctl bootout gui/%d/%s",
		daemonLabelBase, home, computedLabel, os.Getuid(), daemonLabelBase)
}

// plistWorkingDirectoryRe matches the <string> value immediately following
// a <key>WorkingDirectory</key> entry in a launchd plist.
var plistWorkingDirectoryRe = regexp.MustCompile(`(?s)<key>WorkingDirectory</key>\s*<string>(.*?)</string>`)

func extractPlistWorkingDirectory(plistXML string) (string, bool) {
	m := plistWorkingDirectoryRe.FindStringSubmatch(plistXML)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// isBenignBootoutError reports whether a bootout failure is launchctl's
// well-known signature for "the target wasn't loaded" — an expected,
// non-actionable outcome for InstallDaemon's pre-emptive bootout (there's
// nothing to unload on a first install). That signature lives in the
// command's OUTPUT ("Boot-out failed: 3: No such process"), not in
// err.Error() (which for a nonzero exit is just "exit status 3"), so both
// are checked: the output text when available, and exit code 3 as a
// fallback for callers that only have the error. Anything else is a real
// failure worth surfacing.
func isBenignBootoutError(err error, output string) bool {
	if strings.Contains(output, "No such process") || strings.Contains(output, "Could not find") {
		return true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 3 {
		return true
	}
	return false
}

// bootout unloads the launchd job for label, returning the raw combined
// command output alongside any error. The output is the only place the
// "wasn't loaded" vs. "failed to unload" distinction is expressed —
// discarding it (as the original implementation did with `_, err :=`)
// makes that distinction unrecoverable from err alone.
func bootout(label string) (string, error) {
	return runCommand("launchctl", "bootout", launchctlTarget(label))
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
