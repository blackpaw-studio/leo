package service

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

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
//
// The returned io.Closer must be held for the life of the daemon and
// closed on shutdown so lumberjack can finish any in-flight gzip of a
// rotated file.
func installLogRotator(logPath string) (io.Closer, error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("creating log pipe: %w", err)
	}

	w := NewRotatingLogWriter(logPath)

	go func() {
		defer pr.Close()
		_, _ = io.Copy(w, pr)
	}()

	if err := unix.Dup2(int(pw.Fd()), int(os.Stdout.Fd())); err != nil {
		pw.Close()
		return nil, fmt.Errorf("redirecting stdout: %w", err)
	}
	if err := unix.Dup2(int(pw.Fd()), int(os.Stderr.Fd())); err != nil {
		pw.Close()
		return nil, fmt.Errorf("redirecting stderr: %w", err)
	}
	// fds 1 and 2 now hold the only references to the pipe write end.
	_ = pw.Close()
	return w, nil
}
