package service

import (
	"fmt"
	"io"
	"os"
	"syscall"
)

// Holds the active rotating writer for the lifetime of the supervised
// process so the goroutine draining the pipe is never GC'd.
var activeLogRotator io.WriteCloser

// installLogRotator rewires the running process's stdout/stderr so that
// every subsequent write flows through a size-based rotating writer.
//
// It opens an os.Pipe, dup2s the write end onto fds 1 and 2, and starts
// a goroutine that copies the read end into the lumberjack-backed
// writer. The init system (launchd/systemd) may have already pointed
// fds 1/2 at the target file — that fd is overwritten by the dup2s, so
// only the rotator owns the file from here on.
//
// Safe to call once, early in daemon startup. Later writes to os.Stdout
// and os.Stderr are automatically routed through the rotator.
func installLogRotator(logPath string) error {
	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("creating log pipe: %w", err)
	}

	w := NewRotatingLogWriter(logPath)
	activeLogRotator = w

	go func() {
		defer pr.Close()
		_, _ = io.Copy(w, pr)
	}()

	if err := syscall.Dup2(int(pw.Fd()), int(os.Stdout.Fd())); err != nil {
		pw.Close()
		return fmt.Errorf("redirecting stdout: %w", err)
	}
	if err := syscall.Dup2(int(pw.Fd()), int(os.Stderr.Fd())); err != nil {
		pw.Close()
		return fmt.Errorf("redirecting stderr: %w", err)
	}
	// fds 1 and 2 now hold the only references to the pipe write end.
	_ = pw.Close()
	return nil
}
