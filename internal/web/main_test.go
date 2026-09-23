package web

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// TestMain points TMUX_TMPDIR at a private directory and drops the caller's
// TMUX/TMUX_PANE so tests that exercise the real exec seams (Server.New
// defaults to exec.Command) can never open dispatch viewer windows on the
// production `tmux -L leo` server (issue #213).
func TestMain(m *testing.M) {
	os.Exit(runIsolated(m))
}

// runIsolated holds TestMain's body so its deferred cleanup runs before
// os.Exit.
func runIsolated(m *testing.M) int {
	// Parse first: viewer windows re-exec this binary as `dispatch watch`,
	// which fails flag parsing and exits here instead of leaking a tmp dir.
	flag.Parse()
	dir, err := isolateTmux()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolating tmux: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)
	defer killIsolatedLeoServer(dir)
	return m.Run()
}

// isolateTmux lives under /tmp rather than $TMPDIR: macOS's $TMPDIR can push
// tmux's socket path past the 104-byte sun_path limit.
func isolateTmux() (string, error) {
	for _, k := range []string{"TMUX", "TMUX_PANE"} {
		if err := os.Unsetenv(k); err != nil {
			return "", err
		}
	}
	dir, err := os.MkdirTemp("/tmp", isolatedTmuxDirPrefix)
	if err != nil {
		return "", err
	}
	if err := os.Setenv("TMUX_TMPDIR", dir); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// killIsolatedLeoServer tears down the viewer windows tests opened. It
// addresses the socket by explicit path (-S), never -L, so it cannot reach the
// live server even if TMUX_TMPDIR were changed underneath it.
func killIsolatedLeoServer(dir string) {
	socket := leoSocketPath(dir)
	if _, err := os.Stat(socket); err != nil {
		return
	}
	_ = exec.Command(findTmuxPath(), "-S", socket, "kill-server").Run()
}
