package cli

import (
	"bytes"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
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
