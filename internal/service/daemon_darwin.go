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
)

var runCommand = defaultRunCommand

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.LeoPath}}</string>
		<string>chat</string>
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

func daemonLabel(agentName string) string {
	return fmt.Sprintf("com.blackpaw.leo.%s", agentName)
}

func plistPath(agentName string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", daemonLabel(agentName)+".plist")
}

// InstallDaemon writes a launchd plist and bootstraps the service.
func InstallDaemon(sc ServiceConfig) error {
	label := daemonLabel(sc.AgentName)

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

	path := plistPath(sc.AgentName)
	if err := mkdirAll(filepath.Dir(path), 0750); err != nil {
		return fmt.Errorf("creating LaunchAgents directory: %w", err)
	}

	// Unload existing service if present (ignore errors)
	_ = bootout(label, path)

	if err := writeFile(path, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("writing plist: %w", err)
	}

	// Bootstrap the service
	uid := fmt.Sprintf("%d", os.Getuid())
	if _, err := runCommand("launchctl", "bootstrap", "gui/"+uid, path); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w", err)
	}

	return nil
}

// RemoveDaemon stops and removes the launchd service.
func RemoveDaemon(agentName string) error {
	label := daemonLabel(agentName)
	path := plistPath(agentName)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("daemon not installed (no plist found)")
	}

	_ = bootout(label, path)

	if err := removeFile(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing plist: %w", err)
	}

	return nil
}

// DaemonStatus returns the status of the launchd service.
func DaemonStatus(agentName string) (string, error) {
	path := plistPath(agentName)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "not installed", nil
	}

	label := daemonLabel(agentName)
	uid := fmt.Sprintf("%d", os.Getuid())
	target := fmt.Sprintf("gui/%s/%s", uid, label)

	output, err := runCommand("launchctl", "print", target)
	if err != nil {
		return "installed but not running", nil
	}

	// Parse for PID
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "pid = ") {
			pid := strings.TrimPrefix(trimmed, "pid = ")
			return fmt.Sprintf("running (pid %s)", pid), nil
		}
	}

	return "installed", nil
}

// RestartDaemon force-restarts the launchd service.
func RestartDaemon(agentName string) error {
	path := plistPath(agentName)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("daemon not installed (no plist found)")
	}

	label := daemonLabel(agentName)
	uid := fmt.Sprintf("%d", os.Getuid())
	target := fmt.Sprintf("gui/%s/%s", uid, label)

	if _, err := runCommand("launchctl", "kickstart", "-k", target); err != nil {
		return fmt.Errorf("launchctl kickstart: %w", err)
	}

	return nil
}

func bootout(label, path string) error {
	uid := fmt.Sprintf("%d", os.Getuid())
	target := fmt.Sprintf("gui/%s/%s", uid, label)
	_, err := runCommand("launchctl", "bootout", target)
	return err
}

func defaultRunCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}
