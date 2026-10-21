package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/managed"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// writeFakeTmux writes <root>/<ver>/tmux as a script. A script that answers
// only to the exact leo probe argv stands in for a tmux whose server is
// running; one that always fails stands in for a tmux with no server.
func writeFakeTmux(t *testing.T, root, ver, serverVersion string) string {
	t.Helper()
	dir := filepath.Join(root, ver)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\nexit 1\n"
	if serverVersion != "" {
		body = "#!/bin/sh\n[ \"$*\" = \"-L leo display-message -p #{version}\" ] && echo " + serverVersion + " && exit 0\nexit 1\n"
	}
	path := filepath.Join(dir, "tmux")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
	return path
}

// managedCLIFixture points the CLI at a config and a managed marker whose
// tmux_path is the pinned copy under a temp root.
func managedCLIFixture(t *testing.T, pinnedPath string) {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, "leo.yaml")
	if err := config.Save(path, &config.Config{HomePath: home, Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10}}); err != nil {
		t.Fatal(err)
	}
	oldCfg := cfgFile
	cfgFile = path
	t.Cleanup(func() { cfgFile = oldCfg })
	stubManaged(t, managed.State{Managed: true, Marker: managed.Marker{TmuxPath: pinnedPath}})
}

// With a server started from an older pinned copy still running, `leo agent
// attach` must exec that copy's binary: client and server versions must match.
func TestLocalAttachExecsTheRunningServersTmuxWhenUpgradePending(t *testing.T) {
	root := t.TempDir()
	pinned := writeFakeTmux(t, root, "3.6a", "")
	older := writeFakeTmux(t, root, "3.5", "3.5")
	managedCLIFixture(t, pinned)
	oldEnv := tmuxEnv
	tmuxEnv = func() string { return "" }
	t.Cleanup(func() { tmuxEnv = oldEnv })
	var gotPath string
	var gotArgv []string
	old := agentSyscallExec
	agentSyscallExec = func(argv0 string, argv []string, _ []string) error {
		gotPath, gotArgv = argv0, argv
		return nil
	}
	t.Cleanup(func() { agentSyscallExec = old })

	err := attachTmuxSession(config.HostResolution{Localhost: true}, "leo-scratch", attachOptions{})

	if err != nil {
		t.Fatalf("attachTmuxSession: %v", err)
	}
	if gotPath != older {
		t.Errorf("exec path = %q, want the 3.5 server's binary %q", gotPath, older)
	}
	wantArgv := []string{"tmux", "-L", "leo", "attach", "-t", "=leo-scratch"}
	if !reflect.DeepEqual(gotArgv, wantArgv) {
		t.Errorf("argv = %v, want %v", gotArgv, wantArgv)
	}
}

func TestViewerAndAttachShareTheManagedResolution(t *testing.T) {
	root := t.TempDir()
	pinned := writeFakeTmux(t, root, "3.6a", "3.6a")
	managedCLIFixture(t, pinned)

	for name, locate := range map[string]func() (string, error){
		"attach": tmuxLocate, "viewer": viewerLocateTmux,
	} {
		got, err := locate()
		if err != nil || got != pinned {
			t.Errorf("%s: got %q, %v; want the managed %q", name, got, err, pinned)
		}
	}
}

// Without a loadable config the CLI cannot know it is managed, so it falls
// back to the plain lookup rather than failing.
func TestResolveTmuxFallsBackToLocateWhenConfigCannotLoad(t *testing.T) {
	oldCfg := cfgFile
	cfgFile = filepath.Join(t.TempDir(), "missing.yaml")
	t.Cleanup(func() { cfgFile = oldCfg })
	var seen tmux.ResolveOptions
	oldResolve := tmuxResolveFn
	tmuxResolveFn = func(opts tmux.ResolveOptions) tmux.Resolution {
		seen = opts
		return tmux.Resolution{Client: "/usr/bin/tmux", Start: "/usr/bin/tmux"}
	}
	t.Cleanup(func() { tmuxResolveFn = oldResolve })

	got, err := resolveTmux()

	if err != nil || got != "/usr/bin/tmux" {
		t.Fatalf("resolveTmux = %q, %v", got, err)
	}
	if seen.ManagedPath != "" {
		t.Errorf("ManagedPath = %q, want empty", seen.ManagedPath)
	}
}
