//go:build darwin

package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDaemonLabelDefaultHome(t *testing.T) {
	origHome := userHomeDirFn
	defer func() { userHomeDirFn = origHome }()

	realHome := t.TempDir()
	userHomeDirFn = func() (string, error) { return realHome, nil }

	defaultHome := filepath.Join(realHome, ".leo")

	got := daemonLabel(defaultHome)
	want := "com.blackpaw.leo"
	if got != want {
		t.Errorf("daemonLabel(default home) = %q, want %q (regression: would orphan the production install)", got, want)
	}
}

func TestDaemonLabelNonDefaultHomeIsStableAndDistinct(t *testing.T) {
	origHome := userHomeDirFn
	defer func() { userHomeDirFn = origHome }()

	realHome := t.TempDir()
	userHomeDirFn = func() (string, error) { return realHome, nil }

	homeA := filepath.Join(realHome, "checkout-a", ".leo")
	homeB := filepath.Join(realHome, "checkout-b", ".leo")

	labelA1 := daemonLabel(homeA)
	labelA2 := daemonLabel(homeA)
	labelB := daemonLabel(homeB)
	defaultLabel := daemonLabel(filepath.Join(realHome, ".leo"))

	if labelA1 != labelA2 {
		t.Errorf("daemonLabel(homeA) not stable: %q != %q", labelA1, labelA2)
	}
	if labelA1 == labelB {
		t.Errorf("daemonLabel for two different non-default homes collided: %q", labelA1)
	}
	if labelA1 == defaultLabel {
		t.Errorf("non-default home produced the default label %q", labelA1)
	}
	if !strings.HasPrefix(labelA1, "com.blackpaw.leo.") {
		t.Errorf("daemonLabel(homeA) = %q, want prefix com.blackpaw.leo.", labelA1)
	}
}

// TestDaemonLabelSpellingVariantsMatch verifies that equivalent spellings
// of the default home (symlink, trailing slash) all resolve to the same
// label, so a differently-spelled invocation of the default home can
// never silently split one production install into two identities.
func TestDaemonLabelSpellingVariantsMatch(t *testing.T) {
	origHome := userHomeDirFn
	defer func() { userHomeDirFn = origHome }()

	realHome := t.TempDir()
	leoHome := filepath.Join(realHome, ".leo")
	if err := os.MkdirAll(leoHome, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Symlink alias for realHome, mirroring "/Users/evan" vs a symlinked path.
	aliasHome := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realHome, aliasHome); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	userHomeDirFn = func() (string, error) { return realHome, nil }

	want := daemonLabel(leoHome)

	if got := daemonLabel(leoHome + "/"); got != want {
		t.Errorf("daemonLabel with trailing slash = %q, want %q", got, want)
	}
	if got := daemonLabel(filepath.Join(aliasHome, ".leo")); got != want {
		t.Errorf("daemonLabel via symlinked home = %q, want %q", got, want)
	}
}

func TestPlistPath(t *testing.T) {
	origHome := userHomeDirFn
	defer func() { userHomeDirFn = origHome }()

	realHome := t.TempDir()
	userHomeDirFn = func() (string, error) { return realHome, nil }

	defaultHome := filepath.Join(realHome, ".leo")
	got := plistPath(defaultHome)
	want := filepath.Join(realHome, "Library", "LaunchAgents", "com.blackpaw.leo.plist")
	if got != want {
		t.Errorf("plistPath() = %q, want %q", got, want)
	}
}

func TestInstallDaemon(t *testing.T) {
	origMkdir := mkdirAll
	origWrite := writeFile
	origRename := renameFile
	origRun := runCommand
	origHome := userHomeDirFn
	defer func() {
		mkdirAll = origMkdir
		writeFile = origWrite
		renameFile = origRename
		runCommand = origRun
		userHomeDirFn = origHome
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	mkdirAll = func(path string, perm os.FileMode) error { return nil }

	var writtenPath string
	var writtenContent []byte
	writeFile = func(name string, data []byte, perm os.FileMode) error {
		writtenPath = name
		writtenContent = data
		return nil
	}

	var renamedFrom, renamedTo string
	renameFile = func(oldpath, newpath string) error {
		renamedFrom = oldpath
		renamedTo = newpath
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
		WorkDir:    filepath.Join(home, ".leo"),
		LogPath:    "/workspace/state/service.log",
		Env: map[string]string{
			"HOME": "/Users/test",
		},
	}

	err := InstallDaemon(sc)
	if err != nil {
		t.Fatalf("InstallDaemon() error: %v", err)
	}

	// The plist is written to a temp file first, not the final path directly.
	if !strings.HasSuffix(writtenPath, "com.blackpaw.leo.plist.tmp") {
		t.Errorf("plist written to %q, want suffix com.blackpaw.leo.plist.tmp", writtenPath)
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

	// Verify the temp file was renamed into place over the real plist path.
	if renamedFrom != writtenPath {
		t.Errorf("renameFile called with from=%q, want %q", renamedFrom, writtenPath)
	}
	if !strings.HasSuffix(renamedTo, "com.blackpaw.leo.plist") || strings.HasSuffix(renamedTo, ".tmp") {
		t.Errorf("renameFile called with to=%q, want suffix com.blackpaw.leo.plist", renamedTo)
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

// TestInstallDaemonWriteFailureLeavesExistingPlistIntact is the core
// regression test for the destructive-replace bug: a failure writing the
// new plist must never touch the plist that was already on disk. The
// previous implementation deleted the old plist before writing the new
// one, so any failure after that point left launchd with a loaded-but-
// missing-plist ghost.
func TestInstallDaemonWriteFailureLeavesExistingPlistIntact(t *testing.T) {
	origWrite := writeFile
	origRun := runCommand
	origHome := userHomeDirFn
	defer func() {
		writeFile = origWrite
		runCommand = origRun
		userHomeDirFn = origHome
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(launchAgentsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	leoHome := filepath.Join(home, ".leo")
	plistFile := filepath.Join(launchAgentsDir, "com.blackpaw.leo.plist")
	original := []byte("<plist>ORIGINAL</plist>")
	if err := os.WriteFile(plistFile, original, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	runCommand = func(name string, args ...string) (string, error) { return "", nil }
	writeFile = func(name string, data []byte, perm os.FileMode) error {
		return fmt.Errorf("disk full")
	}

	sc := ServiceConfig{
		LeoPath:    "/usr/local/bin/leo",
		ConfigPath: "/workspace/leo.yaml",
		WorkDir:    leoHome,
		LogPath:    filepath.Join(home, "state", "service.log"),
	}

	if err := InstallDaemon(sc); err == nil {
		t.Fatal("expected error from InstallDaemon")
	}

	got, err := os.ReadFile(plistFile)
	if err != nil {
		t.Fatalf("reading plist after failed install: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("original plist was modified: got %q, want %q", got, original)
	}
}

// TestInstallDaemonReplacesAtomically exercises the real filesystem (no
// writeFile/renameFile seams) to confirm a successful install ends with
// the new content on disk and no leftover temp file.
func TestInstallDaemonReplacesAtomically(t *testing.T) {
	origRun := runCommand
	origHome := userHomeDirFn
	defer func() {
		runCommand = origRun
		userHomeDirFn = origHome
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }
	leoHome := filepath.Join(home, ".leo")

	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(launchAgentsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	plistFile := filepath.Join(launchAgentsDir, "com.blackpaw.leo.plist")
	if err := os.WriteFile(plistFile, []byte("<plist>OLD</plist>"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	runCommand = func(name string, args ...string) (string, error) { return "", nil }

	sc := ServiceConfig{
		LeoPath:    "/usr/local/bin/leo-new",
		ConfigPath: "/workspace/leo.yaml",
		WorkDir:    leoHome,
		LogPath:    filepath.Join(home, "state", "service.log"),
	}

	if err := InstallDaemon(sc); err != nil {
		t.Fatalf("InstallDaemon() error: %v", err)
	}

	got, err := os.ReadFile(plistFile)
	if err != nil {
		t.Fatalf("reading plist: %v", err)
	}
	if !strings.Contains(string(got), "/usr/local/bin/leo-new") {
		t.Errorf("plist not updated: %s", got)
	}

	entries, err := os.ReadDir(launchAgentsDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

// TestInstallDaemonBootoutErrorSurfaced verifies that a genuine bootout
// failure (not the benign "wasn't loaded" signature) is written to
// stderr rather than silently discarded, and does not abort the install.
func TestInstallDaemonBootoutErrorSurfaced(t *testing.T) {
	origMkdir := mkdirAll
	origWrite := writeFile
	origRename := renameFile
	origRun := runCommand
	origHome := userHomeDirFn
	origStderr := os.Stderr
	defer func() {
		mkdirAll = origMkdir
		writeFile = origWrite
		renameFile = origRename
		runCommand = origRun
		userHomeDirFn = origHome
		os.Stderr = origStderr
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }
	mkdirAll = func(path string, perm os.FileMode) error { return nil }
	writeFile = func(name string, data []byte, perm os.FileMode) error { return nil }
	renameFile = func(oldpath, newpath string) error { return nil }

	runCommand = func(name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "bootout") {
			return "", fmt.Errorf("Boot-out failed: 5: Input/output error")
		}
		return "", nil
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w

	sc := ServiceConfig{
		WorkDir: filepath.Join(home, ".leo"),
		LogPath: "/workspace/state/service.log",
	}

	if err := InstallDaemon(sc); err != nil {
		t.Fatalf("InstallDaemon() error: %v", err)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("closing pipe writer: %v", err)
	}
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	stderrOutput := string(buf[:n])

	if !strings.Contains(stderrOutput, "bootout") {
		t.Errorf("expected bootout error surfaced on stderr, got: %q", stderrOutput)
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
	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }
	// Simulate launchctl not knowing about the service
	runCommand = func(name string, args ...string) (string, error) {
		return "", fmt.Errorf("service not found")
	}

	status, err := DaemonStatus(filepath.Join(home, ".leo"))
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
	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	runCommand = func(name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "print") {
			return "pid = 67210\nstate = running\n", nil
		}
		return "", nil
	}

	status, err := DaemonStatus(filepath.Join(home, ".leo"))
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
	leoHome := filepath.Join(home, ".leo")

	// Write a plist file but have launchctl fail
	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	os.MkdirAll(launchAgentsDir, 0755)
	plist := filepath.Join(launchAgentsDir, daemonLabel(leoHome)+".plist")
	os.WriteFile(plist, []byte("<plist/>"), 0644)

	runCommand = func(name string, args ...string) (string, error) {
		return "", fmt.Errorf("service not found")
	}

	status, err := DaemonStatus(leoHome)
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
	leoHome := filepath.Join(home, ".leo")

	// Create a fake plist file at the expected path
	label := daemonLabel(leoHome)
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

	status, err := DaemonStatus(leoHome)
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

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }
	// Simulate launchctl with no record of the service
	runCommand = func(name string, args ...string) (string, error) {
		return "", fmt.Errorf("service not found")
	}

	err := RemoveDaemon(filepath.Join(home, ".leo"))
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

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

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

	if err := RemoveDaemon(filepath.Join(home, ".leo")); err != nil {
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
	leoHome := filepath.Join(home, ".leo")

	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(launchAgentsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	plist := filepath.Join(launchAgentsDir, daemonLabel(leoHome)+".plist")
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

	err := RemoveDaemon(leoHome)
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
	leoHome := filepath.Join(home, ".leo")

	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(launchAgentsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	plist := filepath.Join(launchAgentsDir, daemonLabel(leoHome)+".plist")
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

	if err := RemoveDaemon(leoHome); err != nil {
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
	leoHome := filepath.Join(home, ".leo")

	// Create the plist file at the expected path
	label := daemonLabel(leoHome)
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

	err := RemoveDaemon(leoHome)
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
	leoHome := filepath.Join(home, ".leo")

	// Create the plist file
	label := daemonLabel(leoHome)
	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	os.MkdirAll(launchAgentsDir, 0755)
	plist := filepath.Join(launchAgentsDir, label+".plist")
	os.WriteFile(plist, []byte("<plist/>"), 0644)

	// Mock runCommand: "launchctl print" returns error (not loaded)
	runCommand = func(name string, args ...string) (string, error) {
		return "", fmt.Errorf("could not find service")
	}

	status, err := DaemonStatus(leoHome)
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
	leoHome := filepath.Join(home, ".leo")

	// Create the plist file
	label := daemonLabel(leoHome)
	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	os.MkdirAll(launchAgentsDir, 0755)
	plist := filepath.Join(launchAgentsDir, label+".plist")
	os.WriteFile(plist, []byte("<plist/>"), 0644)

	// Mock runCommand: "launchctl print" returns pid
	runCommand = func(name string, args ...string) (string, error) {
		return "pid = 12345\nstate = running\n", nil
	}

	status, err := DaemonStatus(leoHome)
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
	leoHome := filepath.Join(home, ".leo")

	// Create the plist file
	label := daemonLabel(leoHome)
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

	status, err := DaemonStatus(leoHome)
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
	origRename := renameFile
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		mkdirAll = origMkdir
		writeFile = origWrite
		renameFile = origRename
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
	renameFile = func(oldpath, newpath string) error { return nil }

	runCommand = func(name string, args ...string) (string, error) {
		return "", nil
	}

	sc := ServiceConfig{
		LeoPath:    "/usr/local/bin/leo",
		ConfigPath: "/workspace/leo.yaml",
		WorkDir:    filepath.Join(home, ".leo"),
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

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	mkdirAll = func(path string, perm os.FileMode) error { return nil }
	writeFile = func(name string, data []byte, perm os.FileMode) error {
		return fmt.Errorf("disk full")
	}
	runCommand = func(name string, args ...string) (string, error) {
		return "", nil
	}

	sc := ServiceConfig{
		WorkDir: filepath.Join(home, ".leo"),
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
	origRename := renameFile
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		mkdirAll = origMkdir
		writeFile = origWrite
		renameFile = origRename
		runCommand = origRun
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	mkdirAll = func(path string, perm os.FileMode) error { return nil }
	writeFile = func(name string, data []byte, perm os.FileMode) error { return nil }
	renameFile = func(oldpath, newpath string) error { return nil }

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
		WorkDir: filepath.Join(home, ".leo"),
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

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }
	// Simulate launchctl kickstart failing because the service is unknown
	runCommand = func(name string, args ...string) (string, error) {
		return "", fmt.Errorf("service not found")
	}

	err := RestartDaemon(filepath.Join(home, ".leo"))
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

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	var kickstartCalled bool
	runCommand = func(name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "kickstart") {
			kickstartCalled = true
			return "", nil
		}
		return "", nil
	}

	if err := RestartDaemon(filepath.Join(home, ".leo")); err != nil {
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
	leoHome := filepath.Join(home, ".leo")

	// Create the plist file
	label := daemonLabel(leoHome)
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

	err := RestartDaemon(leoHome)
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

func TestDriftDetectedLoadedButMissingPlist(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }
	leoHome := filepath.Join(home, ".leo")

	// No plist on disk, but launchctl reports it loaded and running.
	runCommand = func(name string, args ...string) (string, error) {
		return "pid = 12345\nstate = running\n", nil
	}

	drifted, detail, err := DriftDetected(leoHome)
	if err != nil {
		t.Fatalf("DriftDetected() error: %v", err)
	}
	if !drifted {
		t.Fatal("expected drift to be detected")
	}
	if detail == "" {
		t.Error("expected a non-empty drift detail")
	}
}

func TestDriftDetectedHealthyWithPlist(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }
	leoHome := filepath.Join(home, ".leo")

	label := daemonLabel(leoHome)
	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(launchAgentsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	plist := filepath.Join(launchAgentsDir, label+".plist")
	if err := os.WriteFile(plist, []byte("<plist/>"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	runCommand = func(name string, args ...string) (string, error) {
		return "pid = 12345\nstate = running\n", nil
	}

	drifted, detail, err := DriftDetected(leoHome)
	if err != nil {
		t.Fatalf("DriftDetected() error: %v", err)
	}
	if drifted {
		t.Errorf("expected no drift when plist is present, got detail: %q", detail)
	}
}

func TestDriftDetectedNotLoadedIsNotAFalseAlarm(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }
	leoHome := filepath.Join(home, ".leo")

	// Not loaded, and no plist either — plain "not installed", not drift.
	runCommand = func(name string, args ...string) (string, error) {
		return "", fmt.Errorf("could not find service")
	}

	drifted, detail, err := DriftDetected(leoHome)
	if err != nil {
		t.Fatalf("DriftDetected() error: %v", err)
	}
	if drifted {
		t.Errorf("expected no drift when not loaded, got detail: %q", detail)
	}
}
