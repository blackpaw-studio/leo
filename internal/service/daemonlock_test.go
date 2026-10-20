package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestAcquireDaemonLockContendedReportsHolderPID(t *testing.T) {
	stateDir := t.TempDir()
	release, err := AcquireDaemonLock(stateDir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer func() { _ = release() }()

	_, err = AcquireDaemonLock(stateDir)

	var held *LockHeldError
	if !errors.As(err, &held) {
		t.Fatalf("second acquire error = %v, want *LockHeldError", err)
	}
	if held.PID != os.Getpid() {
		t.Errorf("PID = %d, want %d", held.PID, os.Getpid())
	}
	if want := "another leo daemon owns " + filepath.Dir(stateDir); held.Home != filepath.Dir(stateDir) || !strings.Contains(err.Error(), want) {
		t.Errorf("error %q (home %q) does not name %q", err, held.Home, want)
	}
	if !strings.Contains(err.Error(), "(pid ") {
		t.Errorf("error %q does not include the pid", err)
	}
}

func TestAcquireDaemonLockReacquirableAfterRelease(t *testing.T) {
	stateDir := t.TempDir()
	release, err := AcquireDaemonLock(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	again, err := AcquireDaemonLock(stateDir)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	_ = again()
}

func TestAcquireDaemonLockFDIsCloseOnExec(t *testing.T) {
	// A leaked fd would let the tmux server (and so every agent) inherit the
	// lock and keep it held after the daemon dies.
	stateDir := t.TempDir()
	release, err := AcquireDaemonLock(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()

	f, err := os.Open(filepath.Join(stateDir, daemonLockFile))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	// Find our lock's fd by scanning for the same inode.
	want, _ := f.Stat()
	found := false
	for fd := 3; fd < 256; fd++ {
		var st syscall.Stat_t
		if syscall.Fstat(fd, &st) != nil || st.Ino != want.Sys().(*syscall.Stat_t).Ino || fd == int(f.Fd()) {
			continue
		}
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFD, 0)
		if errno != 0 {
			t.Fatalf("F_GETFD(%d): %v", fd, errno)
		}
		if flags&syscall.FD_CLOEXEC == 0 {
			t.Errorf("lock fd %d lacks FD_CLOEXEC", fd)
		}
		found = true
	}
	if !found {
		t.Fatal("did not find the daemon lock fd")
	}
}

func TestDefaultSupervisedExecRefusesWhenLockHeldBeforeTouchingAnything(t *testing.T) {
	// If the lock wiring ever regressed this would start a real daemon; keep
	// any tmux server it spawns off the default socket directory.
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	home := t.TempDir()
	stateDir := filepath.Join(home, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireDaemonLock(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()

	err = defaultSupervisedExec(RunSupervisedOptions{HomePath: home, ConfigPath: filepath.Join(home, "leo.yaml")})

	var held *LockHeldError
	if !errors.As(err, &held) {
		t.Fatalf("error = %v, want *LockHeldError", err)
	}
	entries, _ := os.ReadDir(stateDir)
	for _, e := range entries {
		if e.Name() != daemonLockFile {
			t.Errorf("losing daemon created %s in the state dir", e.Name())
		}
	}
}

// The marker's env is applied to the daemon's own process environment before
// anything is launched, so agents inherit it. It goes through the setenvFn
// seam, so the test never mutates the real environment.
func TestDefaultSupervisedExecAppliesMarkerEnvThroughSeam(t *testing.T) {
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	home := t.TempDir()
	stateDir := filepath.Join(home, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Hold the lock so the exec stops right after startup env handling.
	release, err := AcquireDaemonLock(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()
	orig := setenvFn
	t.Cleanup(func() { setenvFn = orig })
	var calls [][2]string
	setenvFn = func(k, v string) error { calls = append(calls, [2]string{k, v}); return nil }

	err = defaultSupervisedExec(RunSupervisedOptions{
		HomePath: home, ConfigPath: filepath.Join(home, "leo.yaml"),
		Env: map[string]string{"LEO_B": "2", "LEO_A": "1"},
	})

	var held *LockHeldError
	if !errors.As(err, &held) {
		t.Fatalf("error = %v, want *LockHeldError", err)
	}
	want := [][2]string{{"LEO_A", "1"}, {"LEO_B", "2"}}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("setenv calls = %v, want %v", calls, want)
	}
}
