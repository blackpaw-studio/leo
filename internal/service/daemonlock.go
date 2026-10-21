package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const daemonLockFile = "daemon.lock"

// LockHeldError reports that another daemon already owns the leo home.
type LockHeldError struct {
	Home string
	PID  int
}

func (e *LockHeldError) Error() string {
	return fmt.Sprintf("another leo daemon owns %s (pid %d)", e.Home, e.PID)
}

// AcquireDaemonLock takes an exclusive, non-blocking flock on
// <stateDir>/daemon.lock and records this process's pid in it. The lock lives
// as long as the returned release func is not called and the process runs;
// the kernel drops it on exit, so a crashed daemon never leaves a stale lock.
// The fd is close-on-exec (os.OpenFile's default), so the tmux server leo
// spawns cannot inherit it and keep the lock alive.
func AcquireDaemonLock(stateDir string) (release func() error, err error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating state dir for daemon lock: %w", err)
	}
	path := filepath.Join(stateDir, daemonLockFile)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening daemon lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil { // #nosec G115 -- fd values are small positive ints
		holder := readLockPID(f)
		_ = f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, &LockHeldError{Home: filepath.Dir(stateDir), PID: holder}
		}
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return func() error {
		unlockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN) // #nosec G115 -- fd values are small positive ints
		if closeErr := f.Close(); closeErr != nil {
			return closeErr
		}
		return unlockErr
	}, nil
}

func readLockPID(f *os.File) int {
	buf := make([]byte, 32)
	n, _ := f.ReadAt(buf, 0)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
	return pid
}
