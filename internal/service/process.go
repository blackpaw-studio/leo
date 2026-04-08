package service

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"

	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/blackpaw-studio/leo/internal/daemon"
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
	if err := mkdirAll(stateDir, 0750); err != nil {
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

	if !isRunning(pid) {
		_ = removeFile(pidFile)
		return fmt.Errorf("not running (stale pid file cleaned up)")
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
	if err == nil {
		if isRunning(pid) {
			return fmt.Sprintf("running (pid %d)", pid), nil
		}
		// Stale PID file
		_ = removeFile(pidFile)
	}

	// No valid PID file — check if the daemon IPC socket is alive
	// (covers launchd/systemd-managed processes that don't write a PID file)
	if daemon.IsRunning(workDir) {
		return "running (daemon)", nil
	}

	return "stopped", nil
}

// RunSupervised runs claude in a restart loop with exponential backoff.
// This is invoked when leo chat --supervised is used. It handles SIGTERM/SIGINT
// for graceful shutdown.
func RunSupervised(claudePath string, claudeArgs []string, workDir, configPath string) error {
	return supervisedExecFn(claudePath, claudeArgs, workDir, configPath)
}

func defaultSupervisedExec(claudePath string, claudeArgs []string, workDir, configPath string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Start daemon IPC server
	sockPath := filepath.Join(workDir, "state", "leo.sock")
	srv := daemon.New(sockPath, configPath)
	if err := srv.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: daemon server failed to start: %v\n", err)
	} else {
		defer srv.Shutdown()
		fmt.Fprintf(os.Stdout, "daemon IPC server listening on %s\n", sockPath)
	}

	backoff := initialBackoff

	// Find tmux
	tmuxPath, tmuxErr := exec.LookPath("tmux")
	if tmuxErr != nil {
		// Try common locations
		for _, p := range []string{"/opt/homebrew/bin/tmux", "/usr/local/bin/tmux", "/usr/bin/tmux"} {
			if _, err := os.Stat(p); err == nil {
				tmuxPath = p
				break
			}
		}
		if tmuxPath == "" {
			return fmt.Errorf("tmux not found: install with 'brew install tmux'")
		}
	}

	sessionName := fmt.Sprintf("leo-%d", os.Getpid())

	// Keep a mutable copy of args so we can strip --resume on failure
	currentArgs := make([]string, len(claudeArgs))
	copy(currentArgs, claudeArgs)

	for {
		// Use tmux to provide a real terminal for claude. This is
		// required for plugins (telegram) to communicate with claude
		// via MCP stdio pipes — a Go PTY breaks this communication.
		claudeCmd := strings.Join(append([]string{claudePath}, currentArgs...), " ")

		// Propagate PATH into the tmux session. tmux uses its own global
		// environment, which may not include paths from the launchd plist
		// (e.g. ~/.bun/bin needed by the telegram plugin).
		if p := os.Getenv("PATH"); p != "" {
			claudeCmd = fmt.Sprintf("export PATH=%q; %s", p, claudeCmd)
		}

		// Create a detached tmux session running claude
		createCmd := exec.CommandContext(ctx, tmuxPath,
			"new-session", "-d", "-s", sessionName,
			"-c", workDir,
			"-x", "200", "-y", "50",
			claudeCmd,
		)
		createCmd.Dir = workDir
		createCmd.Env = os.Environ()

		startTime := time.Now()

		if err := createCmd.Run(); err != nil {
			return fmt.Errorf("creating tmux session: %w", err)
		}

		fmt.Fprintf(os.Stdout, "tmux session '%s' created, claude running\n", sessionName)

		// Wait for the tmux session to end (claude exits)
		for {
			select {
			case <-ctx.Done():
				// Kill tmux session on shutdown
				exec.Command(tmuxPath, "kill-session", "-t", sessionName).Run()
				return nil
			case <-time.After(5 * time.Second):
			}

			// Check if session still exists
			check := exec.Command(tmuxPath, "has-session", "-t", sessionName)
			if check.Run() != nil {
				// Session gone — claude exited
				break
			}
		}

		elapsed := time.Since(startTime)

		// Check if we were signaled to stop
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		// If claude exited very quickly, it likely failed to resume a stale
		// session. Strip --resume and clear the session store so the next
		// iteration starts fresh.
		if elapsed < 15*time.Second {
			currentArgs = stripResumeArg(currentArgs)
			clearSessionStore(workDir)
			fmt.Fprintf(os.Stderr, "claude exited quickly (%.0fs), cleared stale session — retrying fresh\n", elapsed.Seconds())
			backoff = initialBackoff
		} else {
			fmt.Fprintf(os.Stderr, "claude exited after %s, restarting in %s\n", elapsed.Round(time.Second), backoff)
			// Exponential backoff with cap
			backoff = time.Duration(math.Min(float64(backoff)*2, float64(maxBackoff)))
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
	}
}

// stripResumeArg removes --resume and its value from claude args.
func stripResumeArg(args []string) []string {
	var result []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--resume" && i+1 < len(args) {
			i++ // skip the value too
			continue
		}
		result = append(result, args[i])
	}
	return result
}

// clearSessionStore removes the stored session so the next launch creates a fresh one.
func clearSessionStore(workDir string) {
	sessFile := filepath.Join(workDir, "state", "sessions.json")
	_ = writeFile(sessFile, []byte("{}"), 0600)
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
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
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
