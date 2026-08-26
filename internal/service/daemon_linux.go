//go:build linux

package service

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

var (
	runCommand    = defaultRunCommand
	userHomeDirFn = os.UserHomeDir
)

const unitTemplate = `[Unit]
Description=Leo service

[Service]
Type=simple
ExecStart={{.LeoPath}} service --supervised --config {{.ConfigPath}}
WorkingDirectory={{.WorkDir}}
Restart=always
RestartSec=5
StandardOutput=append:{{.LogPath}}
StandardError=append:{{.LogPath}}
{{- range $k, $v := .Env}}
Environment="{{$k}}={{$v}}"
{{- end}}

[Install]
WantedBy=default.target
`

type unitData struct {
	LeoPath    string
	ConfigPath string
	WorkDir    string
	LogPath    string
	Env        map[string]string
}

// unitNameBase is the systemd unit name used for the default leo home
// (~/.leo). This exact string MUST NOT change: it is the identity of the
// existing production systemd install, and changing it would orphan that
// unit (systemd would treat it as a brand new, unrelated service).
const unitNameBase = "leo.service"

// unitName derives the systemd user unit name for the given leo home.
//
// Identity scheme mirrors daemonLabel on darwin: the default home
// (~/.leo, resolved) always maps to exactly unitNameBase, preserving
// backward compatibility with every existing production install. Any
// other home gets a deterministic suffix derived from a hash of its
// resolved absolute path: "leo-<12 hex chars of sha256(resolved
// home)>.service". This keeps concurrent installs on one machine from
// ever stopping or overwriting each other's unit.
func unitName(home string) string {
	if isDefaultHome(home, defaultLeoHome()) {
		return unitNameBase
	}
	return fmt.Sprintf("leo-%s.service", homeIdentityHash(home))
}

// defaultLeoHome returns the platform's canonical leo home (~/.leo) using
// the same userHomeDirFn seam as unitPath, so tests can control it and
// production always resolves against the real $HOME.
func defaultLeoHome() string {
	home, err := userHomeDirFn()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".leo")
}

func unitPath(home string) string {
	userHome, _ := userHomeDirFn()
	return filepath.Join(userHome, ".config", "systemd", "user", unitName(home))
}

// InstallDaemon writes a systemd user unit and enables/starts the service.
func InstallDaemon(sc ServiceConfig) error {
	name := unitName(sc.WorkDir)

	if collided, detail := legacyBaseUnitCollision(sc.WorkDir, name); collided {
		fmt.Fprintf(os.Stderr, "warning: %s\n", detail)
	}

	// Ensure state directory exists for log file
	if err := mkdirAll(filepath.Dir(sc.LogPath), 0750); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}

	// Render unit file
	data := unitData(sc)

	tmpl, err := template.New("unit").Parse(unitTemplate)
	if err != nil {
		return fmt.Errorf("parsing unit template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("rendering unit: %w", err)
	}

	path := unitPath(sc.WorkDir)
	if err := mkdirAll(filepath.Dir(path), 0750); err != nil {
		return fmt.Errorf("creating systemd user directory: %w", err)
	}

	// Stop existing service if running (ignore errors)
	_, _ = runCommand("systemctl", "--user", "stop", name)

	// Write to a uniquely-named temp file in the same directory and rename
	// into place atomically, mirroring darwin's plist install: a plain
	// os.WriteFile straight over the target truncates first, so a
	// mid-write failure would leave a truncated/empty unit file on disk
	// while systemd still has the old one loaded — the same drift the
	// temp+rename pattern exists to prevent on darwin.
	tmpPath, err := newTempUnitPath(path)
	if err != nil {
		return fmt.Errorf("creating temp unit file: %w", err)
	}
	if err := writeFile(tmpPath, buf.Bytes(), 0644); err != nil {
		_ = removeFile(tmpPath)
		return fmt.Errorf("writing unit file: %w", err)
	}
	if err := renameFile(tmpPath, path); err != nil {
		_ = removeFile(tmpPath)
		return fmt.Errorf("installing unit file: %w", err)
	}

	if _, err := runCommand("systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}

	if _, err := runCommand("systemctl", "--user", "enable", "--now", name); err != nil {
		return fmt.Errorf("enabling service: %w", err)
	}

	return nil
}

// newTempUnitPath creates a uniquely-named, empty temp file alongside
// path (same directory, so the later rename stays on one filesystem) and
// returns its name.
func newTempUnitPath(path string) (string, error) {
	f, err := createTempFile(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return "", err
	}
	name := f.Name()
	_ = f.Close()
	return name, nil
}

// RemoveDaemon stops and removes the systemd user service for home.
func RemoveDaemon(home string) error {
	path := unitPath(home)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("daemon not installed (no unit file found)")
	}

	name := unitName(home)

	_, _ = runCommand("systemctl", "--user", "disable", "--now", name)

	if err := removeFile(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing unit file: %w", err)
	}

	_, _ = runCommand("systemctl", "--user", "daemon-reload")

	return nil
}

// DaemonStatus returns the status of the systemd user service for home.
//
// systemd is the source of truth, mirroring darwin's DaemonStatus: a
// unit can be active even after its file is deleted from disk (systemd
// keeps it loaded until an explicit daemon-reload), so `systemctl
// is-active` is checked first and the unit file is only consulted when
// systemd reports nothing active.
func DaemonStatus(home string) (string, error) {
	name := unitName(home)

	output, err := runCommand("systemctl", "--user", "is-active", name)
	status := strings.TrimSpace(output)
	if err == nil {
		return status, nil
	}

	if _, statErr := os.Stat(unitPath(home)); os.IsNotExist(statErr) {
		return "not installed", nil
	}

	return fmt.Sprintf("installed (%s)", status), nil
}

// RestartDaemon restarts the systemd user service for home.
func RestartDaemon(home string) error {
	path := unitPath(home)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("daemon not installed (no unit file found)")
	}

	name := unitName(home)
	if _, err := runCommand("systemctl", "--user", "restart", name); err != nil {
		return fmt.Errorf("systemctl restart: %w", err)
	}

	return nil
}

// DriftDetected reports whether the systemd unit for home is active while
// its backing unit file is missing from disk — mirrors darwin's launchd
// drift check. This IS a real, reachable state here: InstallDaemon's
// temp+rename still leaves a window where systemd has daemon-reloaded a
// unit file that could subsequently be deleted out from under it (e.g. a
// manual `rm` of the unit file, or a partial uninstall that removed the
// file but not the running unit).
func DriftDetected(home string) (bool, string) {
	name := unitName(home)
	path := unitPath(home)

	_, err := runCommand("systemctl", "--user", "is-active", name)
	active := err == nil

	_, statErr := os.Stat(path)
	unitExists := statErr == nil

	if active && !unitExists {
		return true, fmt.Sprintf("systemd unit %q is active but its unit file is missing from %s — it will not survive the next logout/reboot", name, path)
	}
	return false, ""
}

// LegacyBaseLabelCollision mirrors darwin's export for callers (leo
// doctor) that want a uniform cross-platform entry point.
func LegacyBaseLabelCollision(home string) (bool, string) {
	return legacyBaseUnitCollision(home, unitName(home))
}

// legacyBaseUnitCollision reports whether a legacy leo.service unit —
// the single shared name every install used before per-home unit
// scoping — is already serving this same home from a non-default
// install path. Detection only, matching darwin's stance: the fix is a
// manual `systemctl --user stop/disable`, never automatic here.
func legacyBaseUnitCollision(home, computedName string) (bool, string) {
	if computedName == unitNameBase {
		return false, ""
	}

	userHome, err := userHomeDirFn()
	if err != nil {
		return false, ""
	}
	baseUnitPath := filepath.Join(userHome, ".config", "systemd", "user", unitNameBase)

	data, err := readFile(baseUnitPath)
	if err != nil {
		return false, ""
	}

	workDir, ok := extractUnitWorkingDirectory(string(data))
	if !ok || resolveHomePath(workDir) != resolveHomePath(home) {
		return false, ""
	}

	return true, fmt.Sprintf(
		"a legacy systemd unit %q is already registered for this leo home (%s). "+
			"Installing under %q would leave two units supervising the same home. "+
			"Recommended fix: systemctl --user disable --now %s",
		unitNameBase, home, computedName, unitNameBase)
}

// unitWorkingDirectoryRe matches the value of a systemd unit file's
// WorkingDirectory= directive.
var unitWorkingDirectoryRe = regexp.MustCompile(`(?m)^WorkingDirectory=(.*)$`)

func extractUnitWorkingDirectory(unitFile string) (string, bool) {
	m := unitWorkingDirectoryRe.FindStringSubmatch(unitFile)
	if m == nil {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

func defaultRunCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}
