package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"

	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/creack/pty/v2"
)

// Testability seams
var (
	startProcess     = defaultStartProcess
	findProcess      = os.FindProcess
	readFile         = os.ReadFile
	writeFile        = os.WriteFile
	removeFile       = os.Remove
	mkdirAll         = os.MkdirAll
	openLogFile      = defaultOpenLogFile
	supervisedExecFn = defaultSupervisedExec
)

const (
	maxBackoff     = 60 * time.Second
	initialBackoff = 5 * time.Second
	stopTimeout    = 5 * time.Second
)

// Start spawns a supervised leo chat process in the background and writes a PID file.
func Start(sc ServiceConfig) error {
	pidFile := PidPath(sc.WorkDir, sc.AgentName)

	// Check if already running
	if pid, err := readPid(pidFile); err == nil {
		if isRunning(pid) {
			return fmt.Errorf("already running (pid %d)", pid)
		}
		// Stale PID file, clean up
		_ = removeFile(pidFile)
	}

	// Ensure state directory exists
	stateDir := filepath.Dir(pidFile)
	if err := mkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}

	// Open log file
	logFile, err := openLogFile(sc.LogPath)
	if err != nil {
		return fmt.Errorf("opening log file: %w", err)
	}

	pid, err := startProcess(sc.LeoPath, sc.ConfigPath, sc.WorkDir, logFile)
	logFile.Close()
	if err != nil {
		return fmt.Errorf("starting process: %w", err)
	}

	if err := writeFile(pidFile, []byte(strconv.Itoa(pid)), 0644); err != nil {
		return fmt.Errorf("writing pid file: %w", err)
	}

	return nil
}

// Stop sends SIGTERM to the supervised process, waits, then SIGKILL if needed.
func Stop(agentName, workDir string) error {
	pidFile := PidPath(workDir, agentName)

	pid, err := readPid(pidFile)
	if err != nil {
		return fmt.Errorf("not running (no pid file)")
	}

	proc, err := findProcess(pid)
	if err != nil {
		_ = removeFile(pidFile)
		return fmt.Errorf("process %d not found", pid)
	}

	// Send SIGTERM
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		_ = removeFile(pidFile)
		return fmt.Errorf("process %d not running", pid)
	}

	// Wait for graceful shutdown
	deadline := time.After(stopTimeout)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			// Force kill
			_ = proc.Signal(syscall.SIGKILL)
			_ = removeFile(pidFile)
			return nil
		case <-ticker.C:
			if !isRunning(pid) {
				_ = removeFile(pidFile)
				return nil
			}
		}
	}
}

// Status returns a human-readable status string for the background process.
func Status(agentName, workDir string) (string, error) {
	pidFile := PidPath(workDir, agentName)

	pid, err := readPid(pidFile)
	if err != nil {
		return "stopped", nil
	}

	if isRunning(pid) {
		return fmt.Sprintf("running (pid %d)", pid), nil
	}

	// Stale PID file
	_ = removeFile(pidFile)
	return "stopped (stale pid file cleaned up)", nil
}

// RunSupervised runs claude in a restart loop with exponential backoff.
// This is invoked when leo chat --supervised is used. It handles SIGTERM/SIGINT
// for graceful shutdown.
func RunSupervised(claudePath string, claudeArgs []string, workDir string) error {
	return supervisedExecFn(claudePath, claudeArgs, workDir)
}

func defaultSupervisedExec(claudePath string, claudeArgs []string, workDir string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	backoff := initialBackoff

	for {
		// Use a PTY so claude runs in interactive mode (required for
		// plugins/channels to load). We monitor output for the trust
		// dialog and auto-accept it by writing Enter.
		cmd := exec.CommandContext(ctx, claudePath, claudeArgs...)
		cmd.Dir = workDir
		cmd.Env = os.Environ()

		ptmx, err := pty.Start(cmd)
		if err != nil {
			return fmt.Errorf("starting claude with pty: %w", err)
		}

		// Copy PTY output to stdout, watching for trust dialog
		done := make(chan error, 1)
		go func() {
			var buf [4096]byte
			var accumulated bytes.Buffer
			for {
				n, readErr := ptmx.Read(buf[:])
				if n > 0 {
					os.Stdout.Write(buf[:n])
					accumulated.Write(buf[:n])

					// Auto-accept workspace trust dialog — escape sequences
				// separate words so we match on just "trust" keyword
					if bytes.Contains(accumulated.Bytes(), []byte("trust")) {
						time.Sleep(500 * time.Millisecond)
						ptmx.Write([]byte("\r"))
						accumulated.Reset()
					}
					// Keep buffer from growing unbounded
					if accumulated.Len() > 8192 {
						accumulated.Reset()
					}
				}
				if readErr != nil {
					if readErr != io.EOF {
						done <- readErr
					} else {
						done <- nil
					}
					return
				}
			}
		}()

		// Wait for command to finish
		cmdErr := cmd.Wait()
		ptmx.Close()
		<-done

		err = cmdErr

		// Check if we were signaled to stop
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		if err == nil {
			// Clean exit, restart with initial backoff
			backoff = initialBackoff
		} else {
			fmt.Fprintf(os.Stderr, "claude exited with error: %v, restarting in %s\n", err, backoff)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}

		// Exponential backoff with cap
		backoff = time.Duration(math.Min(float64(backoff)*2, float64(maxBackoff)))
	}
}

func defaultStartProcess(leoPath, configPath, workDir string, logFile *os.File) (int, error) {
	cmd := exec.Command(leoPath, "chat", "--supervised", "--config", configPath)
	cmd.Dir = workDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		return 0, err
	}

	// Detach — don't wait for the child
	go cmd.Wait()

	return cmd.Process.Pid, nil
}

func defaultOpenLogFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
}

func readPid(path string) (int, error) {
	data, err := readFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func isRunning(pid int) bool {
	proc, err := findProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess always succeeds. Signal 0 checks if process exists.
	return proc.Signal(syscall.Signal(0)) == nil
}
