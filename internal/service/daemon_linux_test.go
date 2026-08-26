//go:build linux

package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitNameDefaultHome(t *testing.T) {
	origHome := userHomeDirFn
	defer func() { userHomeDirFn = origHome }()

	realHome := t.TempDir()
	userHomeDirFn = func() (string, error) { return realHome, nil }

	defaultHome := filepath.Join(realHome, ".leo")

	got := unitName(defaultHome)
	if got != unitNameBase {
		t.Errorf("unitName(default home) = %q, want %q (regression: would orphan the production install)", got, unitNameBase)
	}
}

func TestUnitNameNonDefaultHomeIsStableAndDistinct(t *testing.T) {
	origHome := userHomeDirFn
	defer func() { userHomeDirFn = origHome }()

	realHome := t.TempDir()
	userHomeDirFn = func() (string, error) { return realHome, nil }

	homeA := filepath.Join(realHome, "checkout-a", ".leo")
	homeB := filepath.Join(realHome, "checkout-b", ".leo")

	nameA1 := unitName(homeA)
	nameA2 := unitName(homeA)
	nameB := unitName(homeB)
	defaultName := unitName(filepath.Join(realHome, ".leo"))

	if nameA1 != nameA2 {
		t.Errorf("unitName(homeA) not stable: %q != %q", nameA1, nameA2)
	}
	if nameA1 == nameB {
		t.Errorf("unitName for two different non-default homes collided: %q", nameA1)
	}
	if nameA1 == defaultName {
		t.Errorf("non-default home produced the default unit name %q", nameA1)
	}
	if !strings.HasPrefix(nameA1, "leo-") || !strings.HasSuffix(nameA1, ".service") {
		t.Errorf("unitName(homeA) = %q, want prefix leo- and suffix .service", nameA1)
	}
}

func TestUnitPath(t *testing.T) {
	origHome := userHomeDirFn
	defer func() { userHomeDirFn = origHome }()

	realHome := t.TempDir()
	userHomeDirFn = func() (string, error) { return realHome, nil }

	defaultHome := filepath.Join(realHome, ".leo")
	got := unitPath(defaultHome)
	want := filepath.Join(realHome, ".config", "systemd", "user", unitNameBase)
	if got != want {
		t.Errorf("unitPath() = %q, want %q", got, want)
	}
}

func TestInstallDaemonWritesUnitAtomicallyWithCorrectPerm(t *testing.T) {
	origRun := runCommand
	origHome := userHomeDirFn
	defer func() {
		runCommand = origRun
		userHomeDirFn = origHome
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }
	leoHome := filepath.Join(home, ".leo")

	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	unitFile := filepath.Join(unitDir, unitNameBase)
	if err := os.WriteFile(unitFile, []byte("[Unit]\nOLD\n"), 0644); err != nil {
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

	got, err := os.ReadFile(unitFile)
	if err != nil {
		t.Fatalf("reading unit file: %v", err)
	}
	if !strings.Contains(string(got), "/usr/local/bin/leo-new") {
		t.Errorf("unit file not updated: %s", got)
	}

	// Nit #4: os.CreateTemp always creates 0600; without an explicit
	// Chmod, the temp-file rename pattern would silently downgrade the
	// unit file's permissions on every reinstall.
	info, err := os.Stat(unitFile)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0644 {
		t.Errorf("unit file mode = %o, want 0644", info.Mode().Perm())
	}

	entries, err := os.ReadDir(unitDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestInstallDaemonWriteFailureLeavesExistingUnitIntact(t *testing.T) {
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
	leoHome := filepath.Join(home, ".leo")

	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	unitFile := filepath.Join(unitDir, unitNameBase)
	original := []byte("[Unit]\nORIGINAL\n")
	if err := os.WriteFile(unitFile, original, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	runCommand = func(name string, args ...string) (string, error) { return "", nil }
	writeFile = func(name string, data []byte, perm os.FileMode) error {
		return fmt.Errorf("disk full")
	}

	sc := ServiceConfig{
		WorkDir: leoHome,
		LogPath: filepath.Join(home, "state", "service.log"),
	}

	if err := InstallDaemon(sc); err == nil {
		t.Fatal("expected error from InstallDaemon")
	}

	got, err := os.ReadFile(unitFile)
	if err != nil {
		t.Fatalf("reading unit file after failed install: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("original unit file was modified: got %q, want %q", got, original)
	}
}

func TestDaemonStatusActiveEvenWithoutUnitFile(t *testing.T) {
	// This is the behavioral inversion this round shipped: systemctl
	// is-active is now the source of truth, matching darwin's launchctl
	// print — a unit can be active after its file is deleted from disk.
	origRun := runCommand
	origHome := userHomeDirFn
	defer func() {
		runCommand = origRun
		userHomeDirFn = origHome
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	runCommand = func(name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "is-active") {
			return "active\n", nil
		}
		return "", nil
	}

	status, err := DaemonStatus(filepath.Join(home, ".leo"))
	if err != nil {
		t.Fatalf("DaemonStatus() error: %v", err)
	}
	if status != "active" {
		t.Errorf("status = %q, want %q", status, "active")
	}
}

func TestDaemonStatusNotInstalled(t *testing.T) {
	origRun := runCommand
	origHome := userHomeDirFn
	defer func() {
		runCommand = origRun
		userHomeDirFn = origHome
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	runCommand = func(name string, args ...string) (string, error) {
		return "inactive\n", fmt.Errorf("exit status 3")
	}

	status, err := DaemonStatus(filepath.Join(home, ".leo"))
	if err != nil {
		t.Fatalf("DaemonStatus() error: %v", err)
	}
	if status != "not installed" {
		t.Errorf("status = %q, want %q", status, "not installed")
	}
}

func TestDaemonStatusInstalledButNotActive(t *testing.T) {
	origRun := runCommand
	origHome := userHomeDirFn
	defer func() {
		runCommand = origRun
		userHomeDirFn = origHome
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }
	leoHome := filepath.Join(home, ".leo")

	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(unitDir, unitName(leoHome)), []byte("[Unit]\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	runCommand = func(name string, args ...string) (string, error) {
		return "inactive\n", fmt.Errorf("exit status 3")
	}

	status, err := DaemonStatus(leoHome)
	if err != nil {
		t.Fatalf("DaemonStatus() error: %v", err)
	}
	if status != "installed (inactive)" {
		t.Errorf("status = %q, want %q", status, "installed (inactive)")
	}
}

func TestRemoveDaemonNotInstalled(t *testing.T) {
	origHome := userHomeDirFn
	defer func() { userHomeDirFn = origHome }()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	err := RemoveDaemon(filepath.Join(home, ".leo"))
	if err == nil {
		t.Fatal("expected error for non-installed daemon")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("error = %q, want mention of not installed", err.Error())
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

	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	unitFile := filepath.Join(unitDir, unitName(leoHome))
	if err := os.WriteFile(unitFile, []byte("[Unit]\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	runCommand = func(name string, args ...string) (string, error) { return "", nil }
	removeFile = os.Remove

	if err := RemoveDaemon(leoHome); err != nil {
		t.Fatalf("RemoveDaemon() error: %v", err)
	}
	if _, err := os.Stat(unitFile); !os.IsNotExist(err) {
		t.Error("unit file should have been removed")
	}
}

func TestRestartDaemonNotInstalled(t *testing.T) {
	origHome := userHomeDirFn
	defer func() { userHomeDirFn = origHome }()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }

	err := RestartDaemon(filepath.Join(home, ".leo"))
	if err == nil {
		t.Fatal("expected error for non-installed daemon")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("error = %q, want mention of not installed", err.Error())
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

	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(unitDir, unitName(leoHome)), []byte("[Unit]\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var ranCommands []string
	runCommand = func(name string, args ...string) (string, error) {
		ranCommands = append(ranCommands, fmt.Sprintf("%s %s", name, strings.Join(args, " ")))
		return "", nil
	}

	if err := RestartDaemon(leoHome); err != nil {
		t.Fatalf("RestartDaemon() error: %v", err)
	}

	found := false
	for _, cmd := range ranCommands {
		if strings.Contains(cmd, "restart") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected systemctl restart, got commands: %v", ranCommands)
	}
}

func TestDriftDetectedActiveButMissingUnitFile(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }
	leoHome := filepath.Join(home, ".leo")

	// No unit file on disk, but systemd reports it active.
	runCommand = func(name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "is-active") {
			return "active\n", nil
		}
		return "", nil
	}

	drifted, detail := DriftDetected(leoHome)
	if !drifted {
		t.Fatal("expected drift to be detected")
	}
	if detail == "" {
		t.Error("expected a non-empty drift detail")
	}
}

func TestDriftDetectedHealthyWithUnitFile(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }
	leoHome := filepath.Join(home, ".leo")

	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(unitDir, unitName(leoHome)), []byte("[Unit]\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	runCommand = func(name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "is-active") {
			return "active\n", nil
		}
		return "", nil
	}

	drifted, detail := DriftDetected(leoHome)
	if drifted {
		t.Errorf("expected no drift when unit file is present, got detail: %q", detail)
	}
}

func TestDriftDetectedNotActiveIsNotAFalseAlarm(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
	}()

	home := t.TempDir()
	userHomeDirFn = func() (string, error) { return home, nil }
	leoHome := filepath.Join(home, ".leo")

	runCommand = func(name string, args ...string) (string, error) {
		return "inactive\n", fmt.Errorf("exit status 3")
	}

	drifted, detail := DriftDetected(leoHome)
	if drifted {
		t.Errorf("expected no drift when not active, got detail: %q", detail)
	}
}

// TestLegacyBaseUnitCollisionActiveButMissingUnitFile is the case that
// actually matters, mirroring darwin's equivalent: the legacy leo.service
// unit is still active with its unit file already deleted from disk. A
// unit-file-only detector would report false here.
func TestLegacyBaseUnitCollisionActiveButMissingUnitFile(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
	}()

	realHome := t.TempDir()
	userHomeDirFn = func() (string, error) { return realHome, nil }
	// Deliberately no ~/.config/systemd/user/leo.service on disk.

	nonDefaultHome := filepath.Join(realHome, "second-checkout", ".leo")

	runCommand = func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "show") && strings.Contains(joined, "WorkingDirectory") {
			return fmt.Sprintf("WorkingDirectory=%s\n", nonDefaultHome), nil
		}
		return "", nil
	}

	collided, detail := LegacyBaseLabelCollision(nonDefaultHome)
	if !collided {
		t.Fatal("expected a legacy base-unit collision to be detected from systemctl show, with no unit file on disk")
	}
	if !strings.Contains(detail, "systemctl --user disable --now") {
		t.Errorf("detail = %q, want it to name the exact remediation command", detail)
	}
	if !strings.Contains(detail, unitNameBase) {
		t.Errorf("detail = %q, want it to name the legacy unit", detail)
	}
}

func TestLegacyBaseUnitCollisionFallsBackToUnitFile(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
	}()

	realHome := t.TempDir()
	userHomeDirFn = func() (string, error) { return realHome, nil }
	// systemctl show succeeds but prints an empty value — no active unit.
	runCommand = func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "show") {
			return "WorkingDirectory=\n", nil
		}
		return "", nil
	}

	nonDefaultHome := filepath.Join(realHome, "second-checkout", ".leo")

	unitDir := filepath.Join(realHome, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	legacyUnit := fmt.Sprintf("[Unit]\n[Service]\nWorkingDirectory=%s\n", nonDefaultHome)
	if err := os.WriteFile(filepath.Join(unitDir, unitNameBase), []byte(legacyUnit), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	collided, detail := LegacyBaseLabelCollision(nonDefaultHome)
	if !collided {
		t.Fatal("expected a legacy base-unit collision via the unit-file fallback")
	}
	if !strings.Contains(detail, "systemctl --user disable --now") {
		t.Errorf("detail = %q, want it to name the exact remediation command", detail)
	}
}

func TestLegacyBaseUnitCollisionDifferentHome(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
	}()

	realHome := t.TempDir()
	userHomeDirFn = func() (string, error) { return realHome, nil }

	nonDefaultHome := filepath.Join(realHome, "second-checkout", ".leo")
	unrelatedHome := filepath.Join(realHome, "totally-unrelated", ".leo")

	runCommand = func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "show") {
			return fmt.Sprintf("WorkingDirectory=%s\n", unrelatedHome), nil
		}
		return "", nil
	}

	collided, detail := LegacyBaseLabelCollision(nonDefaultHome)
	if collided {
		t.Errorf("expected no collision for an unrelated legacy home, got detail: %q", detail)
	}
}

// TestLegacyBaseUnitCollisionEmptyWorkingDirectoryIsNotAMatch guards
// against resolveHomePath("") resolving to cwd and coincidentally
// matching the candidate home.
func TestLegacyBaseUnitCollisionEmptyWorkingDirectoryIsNotAMatch(t *testing.T) {
	origHome := userHomeDirFn
	origRun := runCommand
	defer func() {
		userHomeDirFn = origHome
		runCommand = origRun
	}()

	realHome := t.TempDir()
	userHomeDirFn = func() (string, error) { return realHome, nil }
	runCommand = func(name string, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "show") {
			return "WorkingDirectory=\n", nil
		}
		return "", nil
	}

	nonDefaultHome := filepath.Join(realHome, "second-checkout", ".leo")

	unitDir := filepath.Join(realHome, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	legacyUnit := "[Unit]\n[Service]\nWorkingDirectory=\n"
	if err := os.WriteFile(filepath.Join(unitDir, unitNameBase), []byte(legacyUnit), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	collided, detail := LegacyBaseLabelCollision(nonDefaultHome)
	if collided {
		t.Errorf("expected no collision for an empty WorkingDirectory, got detail: %q", detail)
	}
}

func TestLegacyBaseUnitCollisionSameAsComputedNameIsNoop(t *testing.T) {
	origHome := userHomeDirFn
	defer func() { userHomeDirFn = origHome }()

	realHome := t.TempDir()
	userHomeDirFn = func() (string, error) { return realHome, nil }

	defaultHome := filepath.Join(realHome, ".leo")

	collided, detail := LegacyBaseLabelCollision(defaultHome)
	if collided {
		t.Errorf("expected no collision when the computed unit name IS the base name, got detail: %q", detail)
	}
}
