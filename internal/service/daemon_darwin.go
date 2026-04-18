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

func daemonLabel() string {
	return "com.blackpaw.leo"
}

func plistPath() string {
	home, _ := userHomeDirFn()
	return filepath.Join(home, "Library", "LaunchAgents", daemonLabel()+".plist")
}

// InstallDaemon writes a launchd plist and bootstraps the service.
func InstallDaemon(sc ServiceConfig) error {
	label := daemonLabel()

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

	path := plistPath()
	if err := mkdirAll(filepath.Dir(path), 0750); err != nil {
		return fmt.Errorf("creating LaunchAgents directory: %w", err)
	}

	// Unload existing service if present and wait for launchd to finish
	_ = bootout(label, path)
	_ = removeFile(path)

	// launchd may need a moment to fully unregister after bootout.
	// Retry bootstrap a few times with a short delay.
	if err := writeFile(path, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("writing plist: %w", err)
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

// RemoveDaemon stops and removes the launchd service.
//
// Cleans up both halves independently: the launchd registration and the
// plist file. If either exists we attempt removal, and we only return
// "not installed" when both are already gone. This handles drift where
// one side has been cleaned up but not the other — e.g. a prior bootout
// failure that left a ghost registration, or a manually deleted plist.
func RemoveDaemon() error {
	label := daemonLabel()
	path := plistPath()

	uid := fmt.Sprintf("%d", os.Getuid())
	target := fmt.Sprintf("gui/%s/%s", uid, label)

	_, printErr := runCommand("launchctl", "print", target)
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

// DaemonStatus returns the status of the launchd service.
//
// launchctl is the source of truth: a service can remain bootstrapped
// (and running) even after its plist file is deleted from disk, so we
// query launchctl first and only fall back to the plist check when
// launchctl has no record of the service.
func DaemonStatus() (string, error) {
	label := daemonLabel()
	uid := fmt.Sprintf("%d", os.Getuid())
	target := fmt.Sprintf("gui/%s/%s", uid, label)

	output, runErr := runCommand("launchctl", "print", target)
	if runErr == nil {
		for _, line := range strings.Split(output, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "pid = ") {
				pid := strings.TrimPrefix(trimmed, "pid = ")
				return fmt.Sprintf("running (pid %s)", pid), nil
			}
		}
		return "installed", nil
	}

	if _, err := os.Stat(plistPath()); os.IsNotExist(err) {
		return "not installed", nil
	}

	return "installed but not running", nil
}

// RestartDaemon force-restarts the launchd service.
//
// Uses launchctl as the source of truth — a service can be running with
// its plist file missing, so we rely on the kickstart call itself to
// report whether the target is loaded. Only when kickstart fails do we
// consult the plist to produce a clearer "not installed" error.
func RestartDaemon() error {
	label := daemonLabel()
	uid := fmt.Sprintf("%d", os.Getuid())
	target := fmt.Sprintf("gui/%s/%s", uid, label)

	if _, err := runCommand("launchctl", "kickstart", "-k", target); err != nil {
		if _, statErr := os.Stat(plistPath()); os.IsNotExist(statErr) {
			return fmt.Errorf("daemon not installed")
		}
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
