package web

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

// leoSocketPath is where `tmux -L leo` connects for the given TMUX_TMPDIR
// (empty means tmux's default, /tmp).
func leoSocketPath(tmuxTmpDir string) string {
	if tmuxTmpDir == "" {
		tmuxTmpDir = "/tmp"
	}
	return filepath.Join(tmuxTmpDir, "tmux-"+strconv.Itoa(os.Getuid()), tmux.SocketName)
}

func sameDir(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}

// Server.New defaults its exec seams to the real exec.Command, so any test
// that starts a dispatch opens a viewer window through `tmux -L leo`. Unless
// TMUX_TMPDIR points somewhere private, that lands on the production leo
// server (issue #213).
func TestTestsNeverReachLiveLeoTmuxServer(t *testing.T) {
	got := leoSocketPath(os.Getenv("TMUX_TMPDIR"))
	live := leoSocketPath("")
	if sameDir(filepath.Dir(got), filepath.Dir(live)) {
		t.Fatalf("tmux -L %s resolves to the live server socket %s; TestMain must isolate TMUX_TMPDIR", tmux.SocketName, got)
	}
	if v, ok := os.LookupEnv("TMUX"); ok {
		t.Fatalf("TMUX=%q leaks the caller's tmux server into tests; TestMain must unset it", v)
	}
}
