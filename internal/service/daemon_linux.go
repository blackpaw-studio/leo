//go:build linux

package service

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	name := unitName(sc.WorkDir)
	_, _ = runCommand("systemctl", "--user", "stop", name)

	if err := writeFile(path, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("writing unit file: %w", err)
	}

	if _, err := runCommand("systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}

	if _, err := runCommand("systemctl", "--user", "enable", "--now", name); err != nil {
		return fmt.Errorf("enabling service: %w", err)
	}

	return nil
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
func DaemonStatus(home string) (string, error) {
	path := unitPath(home)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "not installed", nil
	}

	name := unitName(home)
	output, err := runCommand("systemctl", "--user", "is-active", name)
	status := strings.TrimSpace(output)

	if err != nil {
		return fmt.Sprintf("installed (%s)", status), nil
	}

	return status, nil
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

// DriftDetected mirrors the darwin launchd drift check for parity, but
// systemd unit files are not deleted out from under an active unit by
// anything in this codebase (no destructive-replace path exists here),
// so there is currently no known drift state to detect. Always reports
// false; kept so callers (leo doctor) can treat both platforms uniformly.
func DriftDetected(home string) (bool, string, error) {
	return false, "", nil
}

func defaultRunCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}
