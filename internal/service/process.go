package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

// ProcessSpec describes a process for the supervisor to manage.
type ProcessSpec struct {
	Name        string
	ClaudeArgs  []string
	WorkDir     string
	HasTelegram bool
}

// ProcessState tracks the runtime state of a supervised process.
type ProcessState struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"` // "running", "restarting", "stopped"
	StartedAt time.Time `json:"started_at"`
	Restarts  int       `json:"restarts"`
}

// Supervisor manages multiple Claude processes.
type Supervisor struct {
	mu     sync.RWMutex
	states map[string]*ProcessState
}

// NewSupervisor creates a new process supervisor.
func NewSupervisor() *Supervisor {
	return &Supervisor{
		states: make(map[string]*ProcessState),
	}
}

// States returns a snapshot of all process states.
// Implements daemon.ProcessStateProvider.
func (s *Supervisor) States() map[string]daemon.ProcessStateInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]daemon.ProcessStateInfo, len(s.states))
	for k, v := range s.states {
		result[k] = daemon.ProcessStateInfo{
			Name:      v.Name,
			Status:    v.Status,
			StartedAt: v.StartedAt,
			Restarts:  v.Restarts,
		}
	}
	return result
}

func (s *Supervisor) setState(name, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.states[name]; ok {
		st.Status = status
	}
}

func (s *Supervisor) initState(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[name] = &ProcessState{
		Name:      name,
		Status:    "starting",
		StartedAt: time.Now(),
	}
}

func (s *Supervisor) incrementRestarts(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.states[name]; ok {
		st.Restarts++
		st.StartedAt = time.Now()
	}
}

// Start spawns a supervised leo service process in the background and writes a PID file.
func Start(sc ServiceConfig) error {
	pidFile := PidPath(sc.WorkDir)

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

	// Rotate existing log before starting
	if err := RotateLog(sc.LogPath); err != nil {
		fmt.Fprintf(os.Stderr, "warning: log rotation failed: %v\n", err)
	}

	// Open log file
	logFile, err := openLogFile(sc.LogPath)
	if err != nil {
		return fmt.Errorf("opening log file: %w", err)
	}
	defer logFile.Close()

	pid, err := startProcess(sc.LeoPath, sc.ConfigPath, sc.WorkDir, logFile)
	if err != nil {
		return fmt.Errorf("starting process: %w", err)
	}

	if err := writeFile(pidFile, []byte(strconv.Itoa(pid)), 0600); err != nil {
		return fmt.Errorf("writing pid file: %w", err)
	}

	return nil
}

// Stop sends SIGTERM to the supervised process, waits, then SIGKILL if needed.
func Stop(workDir string) error {
	pidFile := PidPath(workDir)

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
func Status(workDir string) (string, error) {
	pidFile := PidPath(workDir)

	pid, err := readPid(pidFile)
	if err == nil {
		if isRunning(pid) {
			return fmt.Sprintf("running (pid %d)", pid), nil
		}
		// Stale PID file
		_ = removeFile(pidFile)
	}

	// No valid PID file — check if the daemon IPC socket is alive
	if daemon.IsRunning(workDir) {
		return "running (daemon)", nil
	}

	return "stopped", nil
}

// RunSupervised starts all processes in supervised mode with a restart loop.
// It also starts the daemon IPC server for cron scheduling and process management.
func RunSupervised(claudePath string, processes []ProcessSpec, homePath, configPath string) error {
	return supervisedExecFn(claudePath, processes, homePath, configPath)
}

func defaultSupervisedExec(claudePath string, processes []ProcessSpec, homePath, configPath string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	supervisor := NewSupervisor()

	// Start daemon IPC server with process state provider
	sockPath := filepath.Join(homePath, "state", "leo.sock")
	srv := daemon.New(sockPath, configPath)
	srv.SetProcessProvider(supervisor)
	if err := srv.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: daemon server failed to start: %v\n", err)
	} else {
		defer srv.Shutdown()
		fmt.Fprintf(os.Stdout, "daemon IPC server listening on %s\n", sockPath)
	}

	// Find tmux
	tmuxPath, err := findTmux()
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	for _, proc := range processes {
		wg.Add(1)
		go func(spec ProcessSpec) {
			defer wg.Done()
			superviseProcess(ctx, tmuxPath, claudePath, spec, homePath, supervisor)
		}(proc)
	}

	fmt.Fprintf(os.Stdout, "supervising %d process(es)\n", len(processes))
	wg.Wait()
	return nil
}

// superviseProcess runs a single process in a tmux session with restart loop.
func superviseProcess(ctx context.Context, tmuxPath, claudePath string, spec ProcessSpec, homePath string, sv *Supervisor) {
	sv.initState(spec.Name)

	backoff := initialBackoff
	sessionName := fmt.Sprintf("leo-%s", spec.Name)
	currentArgs := make([]string, len(spec.ClaudeArgs))
	copy(currentArgs, spec.ClaudeArgs)

	for {
		// Clean up orphaned plugin processes if this process uses telegram
		if spec.HasTelegram {
			cleanupOrphanedPlugins()
		}

		sv.setState(spec.Name, "running")

		// Shell-quote each token to prevent injection via config values
		quoted := make([]string, 0, len(currentArgs)+1)
		quoted = append(quoted, shellQuote(claudePath))
		for _, arg := range currentArgs {
			quoted = append(quoted, shellQuote(arg))
		}
		claudeCmd := strings.Join(quoted, " ")

		// Propagate PATH into the tmux session
		if p := os.Getenv("PATH"); p != "" {
			claudeCmd = fmt.Sprintf("export PATH=%s; %s", shellQuote(p), claudeCmd)
		}

		// Kill any stale tmux session with our name
		exec.Command(tmuxPath, "kill-session", "-t", sessionName).Run()

		// Create a detached tmux session running claude
		createCmd := exec.CommandContext(ctx, tmuxPath,
			"new-session", "-d", "-s", sessionName,
			"-c", spec.WorkDir,
			"-x", "200", "-y", "50",
			claudeCmd,
		)
		createCmd.Dir = spec.WorkDir
		createCmd.Env = os.Environ()

		startTime := time.Now()

		if err := createCmd.Run(); err != nil {
			sv.setState(spec.Name, "restarting")
			fmt.Fprintf(os.Stderr, "[%s] tmux new-session failed: %v, retrying in %s\n", spec.Name, err, backoff)
			select {
			case <-ctx.Done():
				sv.setState(spec.Name, "stopped")
				return
			case <-time.After(backoff):
			}
			backoff = time.Duration(math.Min(float64(backoff)*2, float64(maxBackoff)))
			continue
		}

		// Reset backoff after successful session creation
		backoff = initialBackoff

		fmt.Fprintf(os.Stdout, "[%s] tmux session '%s' created, claude running\n", spec.Name, sessionName)

		// Wait for the tmux session to end or the plugin to die
		pluginLockFile := filepath.Join(os.Getenv("HOME"), ".claude", "channels", "telegram", "data", "telegram.lock")
		pluginChecksAfterStartup := 0
		for {
			select {
			case <-ctx.Done():
				exec.Command(tmuxPath, "kill-session", "-t", sessionName).Run()
				sv.setState(spec.Name, "stopped")
				return
			case <-time.After(5 * time.Second):
			}

			// Check if tmux session still exists
			check := exec.Command(tmuxPath, "has-session", "-t", sessionName)
			if check.Run() != nil {
				break
			}

			// Monitor telegram plugin lock file (only for telegram processes)
			if spec.HasTelegram && time.Since(startTime) > 30*time.Second {
				if _, err := os.Stat(pluginLockFile); err != nil {
					pluginChecksAfterStartup++
					if pluginChecksAfterStartup >= 3 {
						fmt.Fprintf(os.Stderr, "[%s] telegram plugin died (lock file gone), restarting session\n", spec.Name)
						exec.Command(tmuxPath, "kill-session", "-t", sessionName).Run()
						break
					}
				} else {
					pluginChecksAfterStartup = 0
				}
			}
		}

		elapsed := time.Since(startTime)

		// Check if we were signaled to stop
		select {
		case <-ctx.Done():
			sv.setState(spec.Name, "stopped")
			return
		default:
		}

		sv.setState(spec.Name, "restarting")
		sv.incrementRestarts(spec.Name)

		// If claude exited very quickly, strip --resume and clear this process's session
		if elapsed < 15*time.Second {
			currentArgs = stripResumeArg(currentArgs)
			clearProcessSession(homePath, spec.Name)
			fmt.Fprintf(os.Stderr, "[%s] claude exited quickly (%.0fs), cleared stale session — retrying fresh\n", spec.Name, elapsed.Seconds())
		} else {
			fmt.Fprintf(os.Stderr, "[%s] claude exited after %s, restarting in %s\n", spec.Name, elapsed.Round(time.Second), backoff)
		}

		select {
		case <-ctx.Done():
			sv.setState(spec.Name, "stopped")
			return
		case <-time.After(initialBackoff):
		}
	}
}

func findTmux() (string, error) {
	tmuxPath, err := exec.LookPath("tmux")
	if err == nil {
		return tmuxPath, nil
	}
	for _, p := range []string{"/opt/homebrew/bin/tmux", "/usr/local/bin/tmux", "/usr/bin/tmux"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("tmux not found: install with 'brew install tmux'")
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

// clearProcessSession removes a single process's stored session so the next launch starts fresh.
// Only affects the named process; other processes' sessions are preserved.
func clearProcessSession(homePath, processName string) {
	sessFile := filepath.Join(homePath, "state", "sessions.json")
	data, err := readFile(sessFile)
	if err != nil {
		return
	}
	var store map[string]json.RawMessage
	if json.Unmarshal(data, &store) != nil {
		return
	}
	delete(store, "process:"+processName)
	updated, err := json.Marshal(store)
	if err != nil {
		return
	}
	_ = writeFile(sessFile, updated, 0600)
}

// shellQuote wraps a string in single quotes with proper escaping.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// cleanupOrphanedPlugins removes stale telegram plugin lock files.
func cleanupOrphanedPlugins() {
	lockFile := filepath.Join(os.Getenv("HOME"), ".claude", "channels", "telegram", "data", "telegram.lock")
	data, err := readFile(lockFile)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		_ = removeFile(lockFile)
		return
	}
	proc, err := findProcess(pid)
	if err != nil {
		_ = removeFile(lockFile)
		return
	}
	if proc.Signal(syscall.Signal(0)) != nil {
		_ = removeFile(lockFile)
	}
}

func defaultStartProcess(leoPath, configPath, workDir string, logFile *os.File) (int, error) {
	cmd := exec.Command(leoPath, "service", "--supervised", "--config", configPath)
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
	return proc.Signal(syscall.Signal(0)) == nil
}
