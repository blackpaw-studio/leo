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

var runCommand = defaultRunCommand

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

func unitName() string {
	return "leo.service"
}

func unitPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", unitName())
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

	path := unitPath()
	if err := mkdirAll(filepath.Dir(path), 0750); err != nil {
		return fmt.Errorf("creating systemd user directory: %w", err)
	}

	// Stop existing service if running (ignore errors)
	name := unitName()
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

// RemoveDaemon stops and removes the systemd user service.
func RemoveDaemon() error {
	path := unitPath()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("daemon not installed (no unit file found)")
	}

	name := unitName()

	_, _ = runCommand("systemctl", "--user", "disable", "--now", name)

	if err := removeFile(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing unit file: %w", err)
	}

	_, _ = runCommand("systemctl", "--user", "daemon-reload")

	return nil
}

// DaemonStatus returns the status of the systemd user service.
func DaemonStatus() (string, error) {
	path := unitPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "not installed", nil
	}

	name := unitName()
	output, err := runCommand("systemctl", "--user", "is-active", name)
	status := strings.TrimSpace(output)

	if err != nil {
		return fmt.Sprintf("installed (%s)", status), nil
	}

	return status, nil
}

// RestartDaemon restarts the systemd user service.
func RestartDaemon() error {
	path := unitPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("daemon not installed (no unit file found)")
	}

	name := unitName()
	if _, err := runCommand("systemctl", "--user", "restart", name); err != nil {
		return fmt.Errorf("systemctl restart: %w", err)
	}

	return nil
}

func defaultRunCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}
