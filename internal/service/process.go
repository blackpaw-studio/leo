package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/tmux"
	"github.com/blackpaw-studio/leo/internal/update"
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

	// quickExitThreshold: elapsed < this triggers a "hard reset" on the
	// assumption the session itself is poison — strip --resume, clear the
	// stored session so the next spawn generates a fresh session ID.
	quickExitThreshold = 15 * time.Second

	// healthyUptimeThreshold: elapsed >= this means the process ran long
	// enough to consider recovered — reset the backoff to initialBackoff.
	// Anything between quickExitThreshold and this keeps growing the backoff.
	healthyUptimeThreshold = 10 * time.Minute

	// exitStderrTailLines: how many trailing stderr lines to copy into
	// <name>-exit.log after a crash.
	exitStderrTailLines = 50
)

// ProcessSpec describes a process for the supervisor to manage.
type ProcessSpec struct {
	Name       string
	ClaudeArgs []string
	WorkDir    string
	Env        map[string]string
	WebPort    string // Leo web UI port for plugin control commands
	// StateDir is where per-process stderr capture + exit-code files are
	// written (typically <home>/state). When empty, capture is skipped.
	StateDir string
}

// ProcessState tracks the runtime state of a supervised process.
type ProcessState struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"` // "running", "restarting", "stopped"
	StartedAt time.Time `json:"started_at"`
	Restarts  int       `json:"restarts"`
	Ephemeral bool      `json:"ephemeral,omitempty"`
}

// Supervisor manages multiple Claude processes.
type Supervisor struct {
	mu           sync.RWMutex
	states       map[string]*ProcessState
	cancels      map[string]context.CancelFunc // per-process cancel functions for ephemeral agents
	reservations map[string]struct{}           // names atomically claimed by ReserveAgent before SpawnAgent
	ctx          context.Context               // parent context from RunSupervised
	tmuxPath     string
	claudePath   string
	homePath     string
	configPath   string
}

// NewSupervisor creates a new process supervisor.
func NewSupervisor() *Supervisor {
	return &Supervisor{
		states:       make(map[string]*ProcessState),
		cancels:      make(map[string]context.CancelFunc),
		reservations: make(map[string]struct{}),
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
			Ephemeral: v.Ephemeral,
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
	if existing, ok := s.states[name]; ok {
		// Preserve fields (e.g. Ephemeral) set before superviseProcess starts
		existing.Status = "starting"
		existing.StartedAt = time.Now()
		return
	}
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

// ReserveAgent atomically claims a name so subsequent concurrent spawns hit a
// collision error without waiting for slow pre-spawn work (git fetch, worktree
// add). Pair with ReleaseAgent on any failure before SpawnAgent, or let
// SpawnAgent consume the reservation on success.
func (s *Supervisor) ReserveAgent(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.states[name]; exists {
		return fmt.Errorf("process %q already exists", name)
	}
	if _, reserved := s.reservations[name]; reserved {
		return fmt.Errorf("process %q already reserved", name)
	}
	s.reservations[name] = struct{}{}
	return nil
}

// ReleaseAgent drops a reservation made by ReserveAgent. Safe to call on a
// name that was never reserved (no-op) so callers don't need to track state.
func (s *Supervisor) ReleaseAgent(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.reservations, name)
}

// SpawnAgent starts an ephemeral process managed by the supervisor.
// The process is not persisted to config — it lives only in memory.
// Implements daemon.AgentManager.
func (s *Supervisor) SpawnAgent(spec daemon.AgentSpawnSpec) error {
	s.mu.Lock()
	if _, exists := s.states[spec.Name]; exists {
		s.mu.Unlock()
		return fmt.Errorf("process %q already exists", spec.Name)
	}
	if s.ctx == nil {
		s.mu.Unlock()
		return fmt.Errorf("supervisor not initialized (no context)")
	}
	// Consume any reservation so the name is owned by states from here on.
	delete(s.reservations, spec.Name)

	childCtx, cancel := context.WithCancel(s.ctx) // #nosec G118 -- cancel stored in s.cancels, called by StopAgent
	s.cancels[spec.Name] = cancel
	s.states[spec.Name] = &ProcessState{
		Name:      spec.Name,
		Status:    "starting",
		StartedAt: time.Now(),
		Ephemeral: true,
	}
	s.mu.Unlock()

	procSpec := ProcessSpec{
		Name:       spec.Name,
		ClaudeArgs: spec.ClaudeArgs,
		WorkDir:    spec.WorkDir,
		Env:        spec.Env,
		WebPort:    spec.WebPort,
	}
	go superviseProcess(childCtx, s.tmuxPath, s.claudePath, procSpec, s.homePath, s)
	return nil
}

// StopAgent stops an ephemeral process and cleans up its tmux session.
func (s *Supervisor) StopAgent(name string) error {
	s.mu.Lock()
	st, exists := s.states[name]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("agent %q not found", name)
	}
	if !st.Ephemeral {
		s.mu.Unlock()
		return fmt.Errorf("%q is not an ephemeral agent", name)
	}
	cancel, hasCancel := s.cancels[name]
	s.mu.Unlock()

	if hasCancel {
		cancel()
	}

	// Kill the tmux session directly
	sessionName := agent.SessionName(name)
	exec.Command(s.tmuxPath, tmux.Args("kill-session", "-t", sessionName)...).Run() //nolint:errcheck

	s.mu.Lock()
	delete(s.states, name)
	delete(s.cancels, name)
	s.mu.Unlock()

	return nil
}

// EphemeralAgents returns a snapshot of all ephemeral agent states.
func (s *Supervisor) EphemeralAgents() map[string]daemon.ProcessStateInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]daemon.ProcessStateInfo)
	for k, v := range s.states {
		if v.Ephemeral {
			result[k] = daemon.ProcessStateInfo{
				Name:      v.Name,
				Status:    v.Status,
				StartedAt: v.StartedAt,
				Restarts:  v.Restarts,
				Ephemeral: true,
			}
		}
	}
	return result
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

	// Open log file. Rotation is handled inside the supervised child
	// via installLogRotator, which replaces this fd with a pipe feeding
	// a size-based rotating writer.
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

	// Route our own stdout/stderr through a size-based rotating writer.
	// Fails open — if setup fails, writes keep going to the existing fd.
	if closer, err := installLogRotator(LogPathFor(homePath)); err != nil {
		fmt.Fprintf(os.Stderr, "warning: log rotation setup failed: %v\n", err)
	} else {
		defer func() { _ = closer.Close() }()
	}

	// Find tmux early so we can cache it
	tmuxPath, err := findTmux()
	if err != nil {
		return err
	}

	supervisor := NewSupervisor()
	supervisor.ctx = ctx
	supervisor.tmuxPath = tmuxPath
	supervisor.claudePath = claudePath
	supervisor.homePath = homePath
	supervisor.configPath = configPath

	// Start daemon IPC server with process state provider
	sockPath := filepath.Join(homePath, "state", "leo.sock")
	srv := daemon.New(sockPath, configPath, supervisor)
	if err := srv.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: daemon server failed to start: %v\n", err)
	} else {
		defer func() { _ = srv.Shutdown() }()
		fmt.Fprintf(os.Stdout, "daemon IPC server listening on %s\n", sockPath)

		// Build the agent.Manager shared by web, daemon, and CLI handlers.
		cfgLoader := func() (*config.Config, error) { return config.Load(configPath) }
		agentMgr := agent.New(cfgLoader, supervisor, tmuxPath)
		srv.SetAgentManager(agentMgr)

		// Start web UI if enabled
		if cfg, err := config.Load(configPath); err == nil {
			if err := srv.StartWeb(cfg, agentMgr); err != nil {
				fmt.Fprintf(os.Stderr, "warning: web UI failed to start: %v\n", err)
			}
		}
	}

	// Restore ephemeral agents from previous run
	restored := RestoreAgents(homePath, tmuxPath, supervisor)
	if restored > 0 {
		fmt.Fprintf(os.Stdout, "restored %d ephemeral agent(s)\n", restored)
	}

	// Sync workspace templates (CLAUDE.md, skills/*.md) with whatever's
	// embedded in this binary. Content-diffed, so it's a no-op when files
	// already match. Called on every daemon start so `brew upgrade` +
	// `leo service restart` (or `leo update` + restart) re-syncs the
	// workspace automatically — no manual step.
	workspacePath := filepath.Join(homePath, "workspace")
	if written, err := update.RefreshWorkspace(workspacePath); err != nil {
		fmt.Fprintf(os.Stderr, "warning: workspace refresh failed: %v\n", err)
	} else if len(written) > 0 {
		fmt.Fprintf(os.Stdout, "refreshed %d workspace file(s)\n", len(written))
	}

	// Validate process workspaces before starting
	for _, proc := range processes {
		if _, err := os.Stat(proc.WorkDir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: [%s] workspace %s does not exist\n", proc.Name, proc.WorkDir)
		}
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
	sessionName := agent.SessionName(spec.Name)
	currentArgs := make([]string, len(spec.ClaudeArgs))
	copy(currentArgs, spec.ClaudeArgs)

	for {
		sv.setState(spec.Name, "running")

		// Clear any prior exit.code so a shell SIGKILL mid-run doesn't leave
		// the previous iteration's code on disk to be misattributed here.
		if spec.StateDir != "" {
			resetExitCode(spec.StateDir, spec.Name)
		}

		claudeCmd := buildClaudeShellCmd(claudePath, currentArgs, tmuxPath, spec, os.Getenv("PATH"), os.Stderr)

		// Kill any stale tmux session with our name
		exec.Command(tmuxPath, tmux.Args("kill-session", "-t", sessionName)...).Run()

		// Create a detached tmux session running claude
		createCmd := exec.CommandContext(ctx, tmuxPath,
			tmux.Args(
				"new-session", "-d", "-s", sessionName,
				"-c", spec.WorkDir,
				"-x", "200", "-y", "50",
				claudeCmd,
			)...,
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

		fmt.Fprintf(os.Stdout, "[%s] tmux session '%s' created, claude running\n", spec.Name, sessionName)

		// If any --dangerously-load-development-channels flags are present,
		// claude will show an interactive confirmation prompt. Dismiss it by
		// sending Enter (the default-highlighted option is "I am using this
		// for local development"). Runs in a goroutine so the restart loop
		// isn't blocked if the prompt never appears.
		if hasDevChannelFlag(currentArgs) {
			fmt.Fprintf(os.Stdout, "[%s] auto-accepting dev-channel prompt\n", spec.Name)
			go func() {
				if err := tmux.AcceptDevChannelPrompt(ctx, tmuxPath, sessionName); err != nil && ctx.Err() == nil {
					fmt.Fprintf(os.Stderr, "[%s] warning: dev-channel auto-accept failed: %v\n", spec.Name, err)
				}
			}()
		}

		if waitForSessionEnd(ctx, tmuxPath, sessionName, spec, startTime) {
			sv.setState(spec.Name, "stopped")
			return
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

		// Very-quick exits suggest a poisoned session: strip --resume and
		// clear the stored session so the next spawn starts fresh.
		if elapsed < quickExitThreshold {
			currentArgs = stripResumeArg(currentArgs)
			clearProcessSession(homePath, spec.Name)
			fmt.Fprintf(os.Stderr, "[%s] claude exited quickly (%.0fs), cleared stale session\n", spec.Name, elapsed.Seconds())
		}

		// Read exit info written by the shell wrapper, compose the per-process
		// post-mortem, and emit the new log line. All of this is best-effort —
		// when StateDir is empty or the shell didn't finish writing (e.g.
		// SIGKILL to tmux), we still log what we can.
		exitCode, codeOK := 0, false
		signal := "none"
		var tail []string
		if spec.StateDir != "" {
			exitCode, codeOK = readExitCode(spec.StateDir, spec.Name)
			if codeOK {
				signal = decodeSignal(exitCode)
			}
			tail = tailLines(processStderrPath(spec.StateDir, spec.Name), exitStderrTailLines)
			_ = writeExitLog(spec.StateDir, spec.Name, exitCode, codeOK, signal, elapsed, tail)
		}
		logProcessExit(os.Stderr, spec.Name, elapsed, backoff, exitCode, codeOK, signal,
			processExitLogPath(spec.StateDir, spec.Name), len(tail) > 0)

		// Sleep the current backoff, then advance for the NEXT iteration.
		// `backoff` starts at initialBackoff on cold start; a run that lasts
		// >= healthyUptimeThreshold resets it, anything shorter doubles it
		// up to maxBackoff. The <15s quick-exit path above also strips
		// --resume but doesn't change backoff — it's purely a session fix.
		select {
		case <-ctx.Done():
			sv.setState(spec.Name, "stopped")
			return
		case <-time.After(backoff):
		}
		backoff = advanceBackoff(backoff, elapsed)
	}
}

// waitForSessionEnd blocks until the tmux session ends or the context is cancelled.
// Returns true if the context was cancelled (should stop).
func waitForSessionEnd(ctx context.Context, tmuxPath, sessionName string, spec ProcessSpec, startTime time.Time) bool {
	_ = startTime // kept in signature for future lifecycle hooks
	for {
		select {
		case <-ctx.Done():
			exec.Command(tmuxPath, tmux.Args("kill-session", "-t", sessionName)...).Run()
			return true
		case <-time.After(5 * time.Second):
		}

		// Check if tmux session still exists
		check := exec.Command(tmuxPath, tmux.Args("has-session", "-t", sessionName)...)
		if check.Run() != nil {
			return false
		}

		// Auto-dismiss the "Resume from summary" prompt that blocks
		// unattended sessions when they exceed the context threshold.
		autoResumePrompt(tmuxPath, sessionName, spec.Name)
	}
}

// autoResumePrompt captures the tmux pane and sends Enter if claude is stuck
// at the "Resume from summary" interactive prompt.
func autoResumePrompt(tmuxPath, sessionName, processName string) {
	out, err := exec.Command(tmuxPath, tmux.Args("capture-pane", "-t", sessionName, "-p", "-S", "-10")...).Output()
	if err != nil {
		return
	}
	pane := string(out)
	if strings.Contains(pane, "Resume from summary") && strings.Contains(pane, "Enter to confirm") {
		fmt.Fprintf(os.Stderr, "[%s] detected resume prompt, auto-accepting 'Resume from summary'\n", processName)
		exec.Command(tmuxPath, tmux.Args("send-keys", "-t", sessionName, "Enter")...).Run()
	}
}

func findTmux() (string, error) {
	return tmux.Locate()
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

// supervisorEnvKeyPattern restricts env-var names to the POSIX subset.
// This mirrors config.envKeyPattern and exists as defense-in-depth for the
// shell-string assembly in buildClaudeShellCmd. Config.Validate() is the
// primary gate; this rejects anything that slips through.
var supervisorEnvKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// supervisorWebPortPattern restricts LEO_WEB_PORT to decimal digits so the
// value can be safely interpolated unquoted. Empty values are handled
// separately by the caller (export is omitted).
var supervisorWebPortPattern = regexp.MustCompile(`^[0-9]+$`)

// buildClaudeShellCmd assembles the shell command string that tmux runs
// to launch claude. Defense-in-depth: every interpolated env key is
// validated against supervisorEnvKeyPattern, and spec.WebPort is
// validated against supervisorWebPortPattern, before being embedded in
// the resulting shell string. Invalid entries are dropped and a warning
// is written to warnOut.
//
// Values are shell-quoted via shellQuote (single-quote safe), so they
// cannot introduce new shell tokens even if they contain metacharacters.
func buildClaudeShellCmd(claudePath string, args []string, tmuxPath string, spec ProcessSpec, pathEnv string, warnOut io.Writer) string {
	quoted := make([]string, 0, len(args)+1)
	quoted = append(quoted, shellQuote(claudePath))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	cmd := strings.Join(quoted, " ")

	// Wrap claude with stderr capture + exit-code persistence so the
	// supervisor can produce a post-mortem after tmux exits. Must happen
	// before exports are prepended so the exit-code capture references
	// claude's exit status, not some earlier export.
	if spec.StateDir != "" && spec.Name != "" {
		stderrPath := processStderrPath(spec.StateDir, spec.Name)
		exitPath := processExitCodePath(spec.StateDir, spec.Name)
		cmd = fmt.Sprintf("%s 2> %s; ec=$?; echo \"$ec\" > %s",
			cmd, shellQuote(stderrPath), shellQuote(exitPath))
	}

	// Propagate PATH into the tmux session
	if pathEnv != "" {
		cmd = fmt.Sprintf("export PATH=%s; %s", shellQuote(pathEnv), cmd)
	}

	// Inject Leo env vars for plugin control commands. Only include
	// LEO_WEB_PORT when it passes the numeric gate; skip otherwise.
	leoExports := fmt.Sprintf("export LEO_PROCESS_NAME=%s; export LEO_TMUX_PATH=%s;",
		shellQuote(spec.Name), shellQuote(tmuxPath))
	if spec.WebPort != "" {
		if supervisorWebPortPattern.MatchString(spec.WebPort) {
			leoExports += fmt.Sprintf(" export LEO_WEB_PORT=%s;", spec.WebPort)
		} else if warnOut != nil {
			fmt.Fprintf(warnOut, "[%s] warning: dropping invalid LEO_WEB_PORT %q\n", spec.Name, spec.WebPort)
		}
	}
	cmd = fmt.Sprintf("%s %s", leoExports, cmd)

	// Add per-process env vars, validating each key. Iteration order is
	// non-deterministic (Go map range); sort for stable output so tests
	// and logs are predictable.
	keys := make([]string, 0, len(spec.Env))
	for k := range spec.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !supervisorEnvKeyPattern.MatchString(k) {
			if warnOut != nil {
				fmt.Fprintf(warnOut, "[%s] warning: dropping invalid env key %q\n", spec.Name, k)
			}
			continue
		}
		cmd = fmt.Sprintf("export %s=%s; %s", k, shellQuote(spec.Env[k]), cmd)
	}
	return cmd
}

// hasDevChannelFlag reports whether the claude arg list contains
// --dangerously-load-development-channels, which triggers an interactive
// confirmation prompt at launch.
func hasDevChannelFlag(args []string) bool {
	for _, a := range args {
		if a == "--dangerously-load-development-channels" {
			return true
		}
	}
	return false
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
