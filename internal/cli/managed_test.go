package cli

import (
	"bytes"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/managed"
)

func stubManaged(t *testing.T, st managed.State) (stateDirs *[]string, stderr *bytes.Buffer) {
	t.Helper()
	oldLoad, oldOut := loadManaged, managedWarnOut
	t.Cleanup(func() {
		loadManaged, managedWarnOut = oldLoad, oldOut
		managedWarnOnce = sync.Once{}
	})
	managedWarnOnce = sync.Once{}
	var dirs []string
	loadManaged = func(stateDir string) managed.State {
		dirs = append(dirs, stateDir)
		return st
	}
	var buf bytes.Buffer
	managedWarnOut = &buf
	return &dirs, &buf
}

func TestManagedStateLoadsFromConfigStateDir(t *testing.T) {
	dirs, _ := stubManaged(t, managed.State{Managed: true, Marker: managed.Marker{SelfPath: "/app/leo"}})
	cfg := &config.Config{HomePath: "/iso/home"}

	st := managedState(cfg)

	if !st.Managed || st.SelfPath != "/app/leo" {
		t.Fatalf("state = %+v", st)
	}
	if len(*dirs) != 1 || (*dirs)[0] != "/iso/home/state" {
		t.Fatalf("loaded from %v, want [/iso/home/state]", *dirs)
	}
}

func TestManagedStateWarnsOncePerProcess(t *testing.T) {
	_, stderr := stubManaged(t, managed.State{Warning: "managed.json ignored: bad"})
	cfg := &config.Config{HomePath: "/iso/home"}

	managedState(cfg)
	managedState(cfg)

	if got, want := stderr.String(), "warning: managed.json ignored: bad\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestSelfExecutableUsesMarkerWhenManaged(t *testing.T) {
	stubManaged(t, managed.State{Managed: true, Marker: managed.Marker{SelfPath: "/app/leo"}})
	got, err := selfExecutable(&config.Config{HomePath: "/h"})()
	if err != nil || got != "/app/leo" {
		t.Fatalf("got %q, %v; want /app/leo", got, err)
	}
}

func TestBuildServiceConfigPersistsSelfPathWhenManaged(t *testing.T) {
	stubManaged(t, managed.State{Managed: true, Marker: managed.Marker{SelfPath: "/app/Support/bin/leo"}})
	cfg := &config.Config{HomePath: t.TempDir()}

	sc, err := buildServiceConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if sc.LeoPath != "/app/Support/bin/leo" {
		t.Fatalf("LeoPath = %q, want the marker self_path", sc.LeoPath)
	}
}

func TestBuildServiceConfigUnmanagedUsesRunningExecutable(t *testing.T) {
	stubManaged(t, managed.State{})
	cfg := &config.Config{HomePath: t.TempDir()}

	sc, err := buildServiceConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.Executable()
	if sc.LeoPath != want {
		t.Fatalf("LeoPath = %q, want os.Executable %q", sc.LeoPath, want)
	}
}

// Under launchd's minimal PATH the daemon only finds claude through the
// marker's PATH, so the marker env must be applied before any lookup.
func TestDaemonClaudeLookupAppliesMarkerEnvBeforeLookup(t *testing.T) {
	bin := t.TempDir()
	fake := bin + "/claude"
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	t.Setenv("PATH", "/nonexistent-leo-test-path")
	stubManaged(t, managed.State{Managed: true, Marker: managed.Marker{Env: map[string]string{"PATH": bin}}})

	look, err := daemonClaudeLookup(&config.Config{HomePath: t.TempDir()})
	if err != nil {
		t.Fatalf("daemonClaudeLookup: %v", err)
	}
	got, err := look()
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got != fake {
		t.Fatalf("claude = %q, want %q", got, fake)
	}
}

// A missing claude must not stop the daemon: building the lookup succeeds, the
// lookup itself reports ErrNotFound, and it finds claude once it is installed.
func TestDaemonClaudeLookupToleratesMissingClaudeAndFindsItLater(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	stubManaged(t, managed.State{})

	look, err := daemonClaudeLookup(&config.Config{HomePath: t.TempDir()})
	if err != nil {
		t.Fatalf("daemonClaudeLookup with no claude installed: %v", err)
	}
	if got, err := look(); !errors.Is(err, claudeharness.ErrNotFound) {
		t.Fatalf("lookup = %q, %v; want ErrNotFound", got, err)
	}

	fake := bin + "/claude"
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	if got, err := look(); err != nil || got != fake {
		t.Fatalf("lookup after install = %q, %v; want %q", got, err, fake)
	}
}
