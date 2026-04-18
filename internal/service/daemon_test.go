//go:build darwin

package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDaemonLabel(t *testing.T) {
	got := daemonLabel()
	want := "com.blackpaw.leo"
	if got != want {
		t.Errorf("daemonLabel() = %q, want %q", got, want)
	}
}

func TestPlistPath(t *testing.T) {
	got := plistPath()
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, "Library", "LaunchAgents", "com.blackpaw.leo.plist")
	if got != want {
		t.Errorf("plistPath() = %q, want %q", got, want)
	}
}

func TestInstallDaemon(t *testing.T) {
	origMkdir := mkdirAll
	origWrite := writeFile
	origRun := runCommand
	defer func() {
		mkdirAll = origMkdir
		writeFile = origWrite
		runCommand = origRun
	}()

	mkdirAll = func(path string, perm os.FileMode) error { return nil }

	var writtenPath string
	var writtenContent []byte
	writeFile = func(name string, data []byte, perm os.FileMode) error {
		writtenPath = name
		writtenContent = data
		return nil
	}

	var ranCommands []string
	runCommand = func(name string, args ...string) (string, error) {
		ranCommands = append(ranCommands, fmt.Sprintf("%s %s", name, strings.Join(args, " ")))
		return "", nil
	}

	sc := ServiceConfig{
		LeoPath:    "/usr/local/bin/leo",
		ConfigPath: "/workspace/leo.yaml",
		WorkDir:    "/workspace",
		LogPath:    "/workspace/state/service.log",
		Env: map[string]string{
			"HOME": "/Users/test",
		},
	}

	err := InstallDaemon(sc)
	if err != nil {
		t.Fatalf("InstallDaemon() error: %v", err)
	}

	// Verify plist was written
	if !strings.HasSuffix(writtenPath, "com.blackpaw.leo.plist") {
		t.Errorf("plist written to %q, want suffix com.blackpaw.leo.plist", writtenPath)
	}

	content := string(writtenContent)
	if !strings.Contains(content, "<string>com.blackpaw.leo</string>") {
		t.Error("plist should contain label")
	}
	if !strings.Contains(content, "<string>/usr/local/bin/leo</string>") {
		t.Error("plist should contain leo path")
	}
	if !strings.Contains(content, "<string>/workspace/leo.yaml</string>") {
		t.Error("plist should contain config path")
	}
	if !strings.Contains(content, "<key>HOME</key>") {
		t.Error("plist should contain environment variables")
	}

	// Verify launchctl bootstrap was called
	foundBootstrap := false
	for _, cmd := range ranCommands {
		if strings.Contains(cmd, "bootstrap") {
			foundBootstrap = true
		}
	}
	if !foundBootstrap {
		t.Errorf("expected launchctl bootstrap, got commands: %v", ranCommands)
	}
}

func TestInstallDaemonMkdirError(t *testing.T) {
	origMkdir := mkdirAll
	defer func() { mkdirAll = origMkdir }()

	mkdirAll = func(path string, perm os.FileMode) error {
		return fmt.Errorf("permission denied")
	}

	sc := ServiceConfig{
		LogPath: "/workspace/state/service.log",
	}

	err := InstallDaemon(sc)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "creating state directory") {
		t.Errorf("error = %q, want mention of state directory", err.Error())
	}
}

func TestDaemonStatusNotInstalled(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
	}()

	// Point to a temp dir so no plist exists
	userHomeDirFn = func() (string, error) { return t.TempDir(), nil }
	// Simulate launchctl not knowing about the service
	runCommand = func(name string, args ...string) (string, error) {
		return "", fmt.Errorf("service not found")
	}

	status, err := DaemonStatus()
	if err != nil {
		t.Fatalf("DaemonStatus() error: %v", err)
	}
	if status != "not installed" {
		t.Errorf("status = %q, want %q", status, "not installed")
	}
}

// TestDaemonStatusRunningWithoutPlist verifies that a daemon bootstrapped
// into launchd is reported as running even when its plist file has been
// removed from disk. launchctl retains the registration after file
// deletion, and status must reflect the live service.
func TestDaemonStatusRunningWithoutPlist(t *testing.T) {
	origRun := runCommand
	origHome := userHomeDirFn
	defer func() {
		runCommand = origRun
		userHomeDirFn = origHome
	}()

	// Temp home with no plist file on disk
	userHomeDirFn = func() (string, error) { return t.TempDir(), nil }

	runCommand = func(name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "print") {
			return "pid = 67210\nstate = running\n", nil
		}
		return "", nil
	}

	status, err := DaemonStatus()
	if err != nil {
		t.Fatalf("DaemonStatus() error: %v", err)
	}
	if !strings.Contains(status, "running") {
		t.Errorf("status = %q, want to contain 'running'", status)
	}
	if !strings.Contains(status, "67210") {
		t.Errorf("status = %q, want to contain pid 67210", status)
	}
}

// TestDaemonStatusInstalledButNotRunning verifies that when launchctl
// doesn't know about the service but a plist file exists on disk, we
// report "installed but not running" rather than "not installed".
func TestDaemonStatusInstalledButNotRunning(t *testing.T) {
	origRun := runCommand
	origHome := userHomeDirFn
	defer func() {
		runCommand = origRun
		userHomeDirFn = origHome
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	// Write a plist file but have launchctl fail
	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	os.MkdirAll(launchAgentsDir, 0755)
	plist := filepath.Join(launchAgentsDir, daemonLabel()+".plist")
	os.WriteFile(plist, []byte("<plist/>"), 0644)

	runCommand = func(name string, args ...string) (string, error) {
		return "", fmt.Errorf("service not found")
	}

	status, err := DaemonStatus()
	if err != nil {
		t.Fatalf("DaemonStatus() error: %v", err)
	}
	if status != "installed but not running" {
		t.Errorf("status = %q, want %q", status, "installed but not running")
	}
}

func TestDaemonStatusRunning(t *testing.T) {
	origRun := runCommand
	origHome := userHomeDirFn
	defer func() {
		runCommand = origRun
		userHomeDirFn = origHome
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	// Create a fake plist file at the expected path
	label := daemonLabel()
	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	os.MkdirAll(launchAgentsDir, 0755)
	plist := filepath.Join(launchAgentsDir, label+".plist")
	os.WriteFile(plist, []byte("<plist/>"), 0644)

	runCommand = func(name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "print") {
			return "pid = 12345\nstate = running\n", nil
		}
		return "", nil
	}

	status, err := DaemonStatus()
	if err != nil {
		t.Fatalf("DaemonStatus() error: %v", err)
	}
	if !strings.Contains(status, "running") {
		t.Errorf("status = %q, want to contain 'running'", status)
	}
}

func TestRemoveDaemonNotInstalled(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
	}()

	userHomeDirFn = func() (string, error) { return t.TempDir(), nil }
	// Simulate launchctl with no record of the service
	runCommand = func(name string, args ...string) (string, error) {
		return "", fmt.Errorf("service not found")
	}

	err := RemoveDaemon()
	if err == nil {
		t.Fatal("expected error for non-installed daemon")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("error = %q, want mention of not installed", err.Error())
	}
}

// TestRemoveDaemonGhostRegistration verifies that a stale launchd
// registration with no plist on disk is still cleaned up by bootout
// rather than being reported as "not installed" and left running.
func TestRemoveDaemonGhostRegistration(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
	}()

	userHomeDirFn = func() (string, error) { return t.TempDir(), nil }

	var bootoutCalled bool
	runCommand = func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "bootout") {
			bootoutCalled = true
			return "", nil
		}
		if strings.Contains(joined, "print") {
			return "pid = 12345\n", nil
		}
		return "", nil
	}

	if err := RemoveDaemon(); err != nil {
		t.Fatalf("RemoveDaemon() error: %v", err)
	}
	if !bootoutCalled {
		t.Error("expected bootout to be called on ghost registration")
	}
}

// TestRemoveDaemonBootoutError verifies that a bootout failure is
// surfaced rather than silently ignored. The previous implementation
// swallowed errors with `_ = bootout(...)`, which could leave the
// registration live while the plist was deleted.
func TestRemoveDaemonBootoutError(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	origRemove := removeFile
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
		removeFile = origRemove
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(launchAgentsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	plist := filepath.Join(launchAgentsDir, daemonLabel()+".plist")
	if err := os.WriteFile(plist, []byte("<plist/>"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var removeCalled bool
	removeFile = func(path string) error {
		removeCalled = true
		return nil
	}

	runCommand = func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "bootout") {
			return "", fmt.Errorf("launchctl bootout: denied")
		}
		if strings.Contains(joined, "print") {
			return "pid = 12345\n", nil
		}
		return "", nil
	}

	err := RemoveDaemon()
	if err == nil {
		t.Fatal("expected error when bootout fails")
	}
	if !strings.Contains(err.Error(), "bootout") {
		t.Errorf("error = %q, want mention of bootout", err.Error())
	}
	if removeCalled {
		t.Error("plist should not be removed when bootout fails — that caused the original ghost-registration bug")
	}
}

// TestRemoveDaemonPlistOnly covers the inverse of ghost registration:
// plist file exists on disk but launchctl has no record. Only the plist
// should be removed; bootout must not be attempted.
func TestRemoveDaemonPlistOnly(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	origRemove := removeFile
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
		removeFile = origRemove
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(launchAgentsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	plist := filepath.Join(launchAgentsDir, daemonLabel()+".plist")
	if err := os.WriteFile(plist, []byte("<plist/>"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var bootoutCalled, removeCalled bool
	removeFile = func(path string) error {
		removeCalled = true
		return nil
	}
	runCommand = func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "bootout") {
			bootoutCalled = true
			return "", nil
		}
		if strings.Contains(joined, "print") {
			return "", fmt.Errorf("could not find service")
		}
		return "", nil
	}

	if err := RemoveDaemon(); err != nil {
		t.Fatalf("RemoveDaemon() error: %v", err)
	}
	if bootoutCalled {
		t.Error("bootout should not be called when launchctl has no registration")
	}
	if !removeCalled {
		t.Error("expected plist file to be removed")
	}
}

func TestRemoveDaemonSuccess(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	origRemove := removeFile
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
		removeFile = origRemove
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	// Create the plist file at the expected path
	label := daemonLabel()
	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	os.MkdirAll(launchAgentsDir, 0755)
	plist := filepath.Join(launchAgentsDir, label+".plist")
	os.WriteFile(plist, []byte("<plist/>"), 0644)

	// Mock runCommand (bootout) to succeed
	runCommand = func(name string, args ...string) (string, error) {
		return "", nil
	}

	// Use real removeFile
	removeFile = os.Remove

	err := RemoveDaemon()
	if err != nil {
		t.Fatalf("RemoveDaemon() error: %v", err)
	}

	// Verify plist file was removed
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Error("plist file should have been removed")
	}
}

func TestDaemonStatusInstalledNotRunning(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	// Create the plist file
	label := daemonLabel()
	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	os.MkdirAll(launchAgentsDir, 0755)
	plist := filepath.Join(launchAgentsDir, label+".plist")
	os.WriteFile(plist, []byte("<plist/>"), 0644)

	// Mock runCommand: "launchctl print" returns error (not loaded)
	runCommand = func(name string, args ...string) (string, error) {
		return "", fmt.Errorf("could not find service")
	}

	status, err := DaemonStatus()
	if err != nil {
		t.Fatalf("DaemonStatus() error: %v", err)
	}
	if status != "installed but not running" {
		t.Errorf("status = %q, want %q", status, "installed but not running")
	}
}

func TestDaemonStatusRunningWithPid(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	// Create the plist file
	label := daemonLabel()
	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	os.MkdirAll(launchAgentsDir, 0755)
	plist := filepath.Join(launchAgentsDir, label+".plist")
	os.WriteFile(plist, []byte("<plist/>"), 0644)

	// Mock runCommand: "launchctl print" returns pid
	runCommand = func(name string, args ...string) (string, error) {
		return "pid = 12345\nstate = running\n", nil
	}

	status, err := DaemonStatus()
	if err != nil {
		t.Fatalf("DaemonStatus() error: %v", err)
	}
	if !strings.Contains(status, "running") {
		t.Errorf("status = %q, want to contain 'running'", status)
	}
	if !strings.Contains(status, "12345") {
		t.Errorf("status = %q, want to contain '12345'", status)
	}
}

func TestDaemonStatusInstalled(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	// Create the plist file
	label := daemonLabel()
	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(launchAgentsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	plist := filepath.Join(launchAgentsDir, label+".plist")
	if err := os.WriteFile(plist, []byte("<plist/>"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Mock runCommand: "launchctl print" succeeds but no pid line —
	// the service is bootstrapped but not currently running.
	runCommand = func(name string, args ...string) (string, error) {
		return "state = not running\n", nil
	}

	status, err := DaemonStatus()
	if err != nil {
		t.Fatalf("DaemonStatus() error: %v", err)
	}
	if status != "installed but not running" {
		t.Errorf("status = %q, want %q", status, "installed but not running")
	}
}

func TestInstallDaemonWithHomeSeam(t *testing.T) {
	origHome := userHomeDirFn
	origMkdir := mkdirAll
	origWrite := writeFile
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		mkdirAll = origMkdir
		writeFile = origWrite
		runCommand = origRun
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }
	mkdirAll = func(path string, perm os.FileMode) error { return nil }

	var writtenPath string
	writeFile = func(name string, data []byte, perm os.FileMode) error {
		writtenPath = name
		return nil
	}

	runCommand = func(name string, args ...string) (string, error) {
		return "", nil
	}

	sc := ServiceConfig{
		LeoPath:    "/usr/local/bin/leo",
		ConfigPath: "/workspace/leo.yaml",
		WorkDir:    "/workspace",
		LogPath:    "/workspace/state/service.log",
	}

	err := InstallDaemon(sc)
	if err != nil {
		t.Fatalf("InstallDaemon() error: %v", err)
	}

	expectedPlistDir := filepath.Join(home, "Library", "LaunchAgents")
	if !strings.HasPrefix(writtenPath, expectedPlistDir) {
		t.Errorf("plist written to %q, want prefix %q", writtenPath, expectedPlistDir)
	}
}

func TestInstallDaemonWriteError(t *testing.T) {
	origHome := userHomeDirFn
	origMkdir := mkdirAll
	origWrite := writeFile
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		mkdirAll = origMkdir
		writeFile = origWrite
		runCommand = origRun
	}()

	userHomeDirFn = func() (string, error) { return t.TempDir(), nil }

	mkdirAll = func(path string, perm os.FileMode) error { return nil }
	writeFile = func(name string, data []byte, perm os.FileMode) error {
		return fmt.Errorf("disk full")
	}
	runCommand = func(name string, args ...string) (string, error) {
		return "", nil
	}

	sc := ServiceConfig{
		LogPath: "/workspace/state/service.log",
	}

	err := InstallDaemon(sc)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "writing plist") {
		t.Errorf("error = %q, want mention of writing plist", err.Error())
	}
}

func TestInstallDaemonBootstrapError(t *testing.T) {
	origHome := userHomeDirFn
	origMkdir := mkdirAll
	origWrite := writeFile
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		mkdirAll = origMkdir
		writeFile = origWrite
		runCommand = origRun
	}()

	userHomeDirFn = func() (string, error) { return t.TempDir(), nil }

	mkdirAll = func(path string, perm os.FileMode) error { return nil }
	writeFile = func(name string, data []byte, perm os.FileMode) error { return nil }

	callCount := 0
	runCommand = func(name string, args ...string) (string, error) {
		callCount++
		if callCount == 1 {
			// bootout call - succeed
			return "", nil
		}
		// bootstrap call - fail
		return "", fmt.Errorf("service already loaded")
	}

	sc := ServiceConfig{
		LogPath: "/workspace/state/service.log",
	}

	err := InstallDaemon(sc)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "launchctl bootstrap") {
		t.Errorf("error = %q, want mention of launchctl bootstrap", err.Error())
	}
}

func TestRestartDaemonNotInstalled(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
	}()

	userHomeDirFn = func() (string, error) { return t.TempDir(), nil }
	// Simulate launchctl kickstart failing because the service is unknown
	runCommand = func(name string, args ...string) (string, error) {
		return "", fmt.Errorf("service not found")
	}

	err := RestartDaemon()
	if err == nil {
		t.Fatal("expected error for non-installed daemon")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("error = %q, want mention of not installed", err.Error())
	}
}

// TestRestartDaemonWithoutPlist verifies that a registered service with
// no plist on disk can still be kickstarted — status and restart both
// treat launchctl as the source of truth.
func TestRestartDaemonWithoutPlist(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
	}()

	userHomeDirFn = func() (string, error) { return t.TempDir(), nil }

	var kickstartCalled bool
	runCommand = func(name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "kickstart") {
			kickstartCalled = true
			return "", nil
		}
		return "", nil
	}

	if err := RestartDaemon(); err != nil {
		t.Fatalf("RestartDaemon() error: %v", err)
	}
	if !kickstartCalled {
		t.Error("expected kickstart to be called")
	}
}

func TestRestartDaemonSuccess(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	// Create the plist file
	label := daemonLabel()
	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	os.MkdirAll(launchAgentsDir, 0755)
	plist := filepath.Join(launchAgentsDir, label+".plist")
	os.WriteFile(plist, []byte("<plist/>"), 0644)

	// Mock runCommand to succeed
	var ranCommands []string
	runCommand = func(name string, args ...string) (string, error) {
		ranCommands = append(ranCommands, fmt.Sprintf("%s %s", name, strings.Join(args, " ")))
		return "", nil
	}

	err := RestartDaemon()
	if err != nil {
		t.Fatalf("RestartDaemon() error: %v", err)
	}

	// Verify kickstart was called
	foundKickstart := false
	for _, cmd := range ranCommands {
		if strings.Contains(cmd, "kickstart") {
			foundKickstart = true
		}
	}
	if !foundKickstart {
		t.Errorf("expected launchctl kickstart, got commands: %v", ranCommands)
	}
}
