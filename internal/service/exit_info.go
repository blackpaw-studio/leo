package service

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// processStderrPath returns the per-process stderr log path used by the
// supervisor's shell wrapper to capture claude's stderr.
func processStderrPath(stateDir, name string) string {
	return filepath.Join(stateDir, name+"-stderr.log")
}

// processExitCodePath returns the per-process exit-code file path the shell
// wrapper writes to after claude exits.
func processExitCodePath(stateDir, name string) string {
	return filepath.Join(stateDir, name+"-exit.code")
}

// processExitLogPath returns the per-process post-mortem log the supervisor
// composes from the exit code plus trailing stderr lines.
func processExitLogPath(stateDir, name string) string {
	return filepath.Join(stateDir, name+"-exit.log")
}

// readExitCode reads the shell-written exit-code file for a process. Returns
// (code, true) on success, (0, false) if the file is missing or unparseable
// (e.g. tmux killed the session before the shell could write it).
func readExitCode(stateDir, name string) (int, bool) {
	data, err := os.ReadFile(processExitCodePath(stateDir, name))
	if err != nil {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, false
	}
	return code, true
}

// decodeSignal returns a human-readable signal description given a shell exit
// code. Shells set $? to 128+N when the process dies from signal N. Returns
// "none" for normal exits (code <= 128).
func decodeSignal(exitCode int) string {
	if exitCode <= 128 {
		return "none"
	}
	sig := syscall.Signal(exitCode - 128)
	// syscall.Signal's String() returns "signal 9" for unknown signals and
	// the canonical name ("killed") for known ones on macOS/Linux. Prefer
	// the SIGNAME form when we recognize it, else fall back to the raw
	// number so operators aren't confused by platform-specific names.
	switch sig {
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGILL:
		return "SIGILL"
	case syscall.SIGABRT:
		return "SIGABRT"
	case syscall.SIGBUS:
		return "SIGBUS"
	case syscall.SIGFPE:
		return "SIGFPE"
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGSEGV:
		return "SIGSEGV"
	case syscall.SIGPIPE:
		return "SIGPIPE"
	case syscall.SIGALRM:
		return "SIGALRM"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGHUP:
		return "SIGHUP"
	}
	return fmt.Sprintf("signal=%d", exitCode-128)
}

// tailLines returns up to n trailing lines from a file. Missing or unreadable
// files return an empty slice. Does not distinguish "missing" from "empty" —
// the caller treats either the same way.
func tailLines(path string, n int) []string {
	if n <= 0 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	if len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}

// writeExitLog composes the per-process post-mortem file from the exit code,
// decoded signal, elapsed runtime, and trailing stderr lines. Overwritten on
// each crash — only the most recent matters for operators.
func writeExitLog(stateDir, name string, exitCode int, codeOK bool, signal string, elapsed time.Duration, tail []string) error {
	if stateDir == "" {
		return nil
	}
	if err := os.MkdirAll(stateDir, 0o750); err != nil {
		return fmt.Errorf("mkdir state: %w", err)
	}
	path := processExitLogPath(stateDir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open exit log: %w", err)
	}
	defer f.Close()

	exitStr := "?"
	if codeOK {
		exitStr = strconv.Itoa(exitCode)
	}
	fmt.Fprintf(f, "%s: exit=%s signal=%s after %s at %s\n",
		name, exitStr, signal, elapsed.Round(time.Second), time.Now().Format(time.RFC3339))
	fmt.Fprintf(f, "--- last %d lines of stderr ---\n", len(tail))
	for _, line := range tail {
		fmt.Fprintln(f, line)
	}
	return nil
}

// advanceBackoff returns the backoff duration to use for the NEXT restart,
// given how long the just-ended run lasted and the current backoff. Three
// bands:
//
//	elapsed < healthyUptimeThreshold  →  min(current*2, maxBackoff)
//	elapsed ≥ healthyUptimeThreshold  →  reset to initialBackoff
//
// The first retry after startup uses the initial backoff unchanged — doubling
// only happens for subsequent restarts. Staleness recovery (strip --resume,
// clear session) for elapsed < quickExitThreshold lives at the call site
// because it mutates shared state.
func advanceBackoff(current, elapsed time.Duration) time.Duration {
	if elapsed >= healthyUptimeThreshold {
		return initialBackoff
	}
	next := current * 2
	if next > maxBackoff {
		next = maxBackoff
	}
	return next
}

// logProcessExit emits the supervisor's "claude exited" log line in the new
// format, plus an optional second line pointing to the exit log when we
// actually captured stderr. Writes to out (typically os.Stderr).
func logProcessExit(out io.Writer, name string, elapsed, backoff time.Duration, exitCode int, codeOK bool, signal string, exitLogPath string, haveTail bool) {
	exitStr := "?"
	if codeOK {
		exitStr = strconv.Itoa(exitCode)
	}
	fmt.Fprintf(out, "[%s] claude exited after %s (exit=%s, signal=%s), restarting in %s\n",
		name, elapsed.Round(time.Second), exitStr, signal, backoff)
	if haveTail && exitLogPath != "" {
		fmt.Fprintf(out, "[%s] last %d lines of stderr written to %s\n",
			name, exitStderrTailLines, exitLogPath)
	}
}
