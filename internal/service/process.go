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
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/leomcp"
	"github.com/blackpaw-studio/leo/internal/observe"
	"github.com/blackpaw-studio/leo/internal/tmux"
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

// sessionPollInterval is how often waitForSessionEnd checks the tmux session.
// A package var (not a const) so tests can shorten it.
var sessionPollInterval = 5 * time.Second

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
	// WebToken is the daemon's API bearer token. The supervised Claude
	// process reads it from LEO_API_TOKEN and the built-in MCP server
	// uses it to authenticate against /api/* and /web/*.
	WebToken string
	// StateDir is where per-process stderr capture + exit-code files are
	// written (typically <home>/state). When empty, capture is skipped.
	StateDir string
	// Adopt asks the supervise loop to re-attach to an already-running tmux
	// session on its first iteration instead of killing and recreating it.
	// See agent.SpawnRequest.Adopt for the rationale.
	Adopt bool
	// Harness is the resolved harness adapter name (e.g. "claude", "codex").
	// Empty means "claude" — old state/records predate this field.
	Harness string
	// Kind identifies which leo primitive this spec represents. Empty means
	// an old state/record that predates this field.
	Kind harness.Kind
	// OpeningPrompt carries the opening turn for non-claude harnesses, whose
	// tmux-TUI driver injects it into the pane via Start rather than passing
	// it as a trailing positional arg. Empty for claude, which keeps the
	// prompt in ClaudeArgs.
	OpeningPrompt string
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
	identities   map[string]*procIdentity      // live identity handle per ephemeral agent, re-keyed on rename
	ctx          context.Context               // parent context from RunSupervised
	tmuxPath     string
	claudePath   string
	homePath     string
	configPath   string
	// publisher announces agent lifecycle transitions on the observability
	// event bus. nil (the default for every existing caller) makes publish a
	// no-op — see SetPublisher.
	publisher observe.Publisher
}

// NewSupervisor creates a new process supervisor. The context parameter is
// the parent context shared by every supervised process; it must outlive the
// supervisor and be cancelled at shutdown to drain running processes. Passing
// it at construction time (rather than setting s.ctx later) keeps the field
// invariant under s.mu and eliminates a write-after-publish race.
func NewSupervisor(ctx context.Context) *Supervisor {
	return &Supervisor{
		states:       make(map[string]*ProcessState),
		cancels:      make(map[string]context.CancelFunc),
		reservations: make(map[string]struct{}),
		identities:   make(map[string]*procIdentity),
		ctx:          ctx,
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
	st, ok := s.states[name]
	if !ok {
		s.mu.Unlock()
		return
	}
	st.Status = status
	restarts := st.Restarts
	s.mu.Unlock()

	s.publish(observe.Event{
		Type: observe.EventAgentStateChanged,
		Payload: &observe.AgentStateChangedPayload{
			Agent:    name,
			Status:   observe.MapStatus(status),
			Restarts: restarts,
		},
	})
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
	st, ok := s.states[name]
	if !ok {
		s.mu.Unlock()
		return
	}
	st.Restarts++
	st.StartedAt = time.Now()
	restarts := st.Restarts
	status := st.Status
	s.mu.Unlock()

	s.publish(observe.Event{
		Type: observe.EventAgentStateChanged,
		Payload: &observe.AgentStateChangedPayload{
			Agent:    name,
			Status:   observe.MapStatus(status),
			Restarts: restarts,
		},
	})
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
	id := newProcIdentity(spec.Name, spec.ClaudeArgs)
	s.identities[spec.Name] = id
	spawnedAt := s.states[spec.Name].StartedAt
	s.mu.Unlock()

	// A resumed agent (agent.SpawnRequest.Resumed) already exists from a
	// consumer's point of view — SuspendAgent announced it going to sleep,
	// not leaving — so this respawn is a state transition back, not a new
	// agent appearing. Publishing agent_spawned here would make a
	// stream-only consumer treat a resume as a brand-new agent (wrong) and
	// leave it with no way to tell "suspended" from "gone" (see SuspendAgent).
	if spec.Resumed {
		s.publish(observe.Event{
			Type: observe.EventAgentStateChanged,
			Payload: &observe.AgentStateChangedPayload{
				Agent:  spec.Name,
				Status: observe.StatusStarting,
			},
		})
	} else {
		s.publish(observe.Event{
			Type: observe.EventAgentSpawned,
			Payload: &observe.AgentSpawnedPayload{
				Agent: s.spawnedAgentView(spec, spawnedAt),
			},
		})
	}

	procSpec := ProcessSpec{
		Name:          spec.Name,
		ClaudeArgs:    spec.ClaudeArgs,
		WorkDir:       spec.WorkDir,
		Env:           spec.Env,
		WebPort:       spec.WebPort,
		WebToken:      spec.WebToken,
		Adopt:         spec.Adopt,
		Harness:       spec.Harness,
		Kind:          harness.KindAgent,
		OpeningPrompt: spec.OpeningPrompt,
	}
	go superviseProcess(childCtx, s.tmuxPath, s.claudePath, procSpec, s.homePath, s, id)
	return nil
}

// StopAgent stops an ephemeral process and cleans up its tmux session,
// announcing the agent as gone (observe.EventAgentStopped). Use SuspendAgent
// instead when the agent is expected to come back — see its doc comment.
func (s *Supervisor) StopAgent(name string) error {
	if err := s.stopAgentProcess(name); err != nil {
		return err
	}
	s.publish(observe.Event{
		Type:    observe.EventAgentStopped,
		Payload: &observe.AgentStoppedPayload{Agent: name},
	})
	return nil
}

// SuspendAgent stops an ephemeral process and cleans up its tmux session
// exactly like StopAgent, but announces the transition as
// observe.EventAgentStateChanged{Status: "suspended"} rather than
// observe.EventAgentStopped. A suspended agent is coming back (see
// agent.SpawnRequest.Resumed, published by SpawnAgent as the matching
// transition back) — a consumer must be able to tell that apart from an
// agent that left supervision for good.
func (s *Supervisor) SuspendAgent(name string) error {
	// Read Restarts before stopAgentProcess deletes the state entry — a
	// crash-looped agent's real count would otherwise be silently clobbered
	// to 0 in the published payload.
	s.mu.RLock()
	restarts := 0
	if st, ok := s.states[name]; ok {
		restarts = st.Restarts
	}
	s.mu.RUnlock()

	if err := s.stopAgentProcess(name); err != nil {
		return err
	}
	s.publish(observe.Event{
		Type: observe.EventAgentStateChanged,
		Payload: &observe.AgentStateChangedPayload{
			Agent:    name,
			Status:   observe.StatusSuspended,
			Restarts: restarts,
		},
	})
	return nil
}

// stopAgentProcess cancels the ephemeral process's context, kills its tmux
// session, and removes it from every live-state map. Shared by StopAgent and
// SuspendAgent, which differ only in which event they publish afterward.
func (s *Supervisor) stopAgentProcess(name string) error {
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
	exec.Command(s.tmuxPath, tmux.Args("kill-session", "-t", tmux.Target(sessionName))...).Run() //nolint:errcheck

	s.mu.Lock()
	delete(s.states, name)
	delete(s.cancels, name)
	delete(s.identities, name)
	s.mu.Unlock()

	return nil
}

// RenameAgent renames a running ephemeral agent with zero process restart. It
// renames the tmux session, swaps the live identity handle (so the supervise
// goroutine follows the new name), and re-keys the states/cancels/identities
// maps. A non-running agent (mid-restart) returns a retryable error so callers
// do not race the goroutine's create window.
func (s *Supervisor) RenameAgent(oldName, newName string) error {
	s.mu.Lock()

	st, ok := s.states[oldName]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("agent %q not found", oldName)
	}
	if !st.Ephemeral {
		s.mu.Unlock()
		return fmt.Errorf("%q is not an ephemeral agent", oldName)
	}
	if st.Status != "running" {
		s.mu.Unlock()
		return fmt.Errorf("agent %q is %s, not running; retry once it settles", oldName, st.Status)
	}
	if _, exists := s.states[newName]; exists {
		s.mu.Unlock()
		return fmt.Errorf("agent %q already exists", newName)
	}
	if _, reserved := s.reservations[newName]; reserved {
		s.mu.Unlock()
		return fmt.Errorf("agent %q is reserved", newName)
	}
	id, ok := s.identities[oldName]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("agent %q has no identity handle", oldName)
	}

	// Hold the identity write-lock across the tmux rename + name swap so the
	// watcher's RLock observes either (old,old) or (new,new), never a crossed
	// state. tmux rename-session keeps the running pane alive.
	id.mu.Lock()
	if err := tmuxRenameSession(s.tmuxPath, agent.SessionName(oldName), agent.SessionName(newName)); err != nil {
		id.mu.Unlock()
		s.mu.Unlock()
		return fmt.Errorf("renaming tmux session: %w", err)
	}
	id.renameLocked(newName)
	id.mu.Unlock()

	st.Name = newName
	s.states[newName] = st
	s.cancels[newName] = s.cancels[oldName]
	s.identities[newName] = id
	delete(s.states, oldName)
	delete(s.cancels, oldName)
	delete(s.identities, oldName)
	restarts := st.Restarts
	startedAt := st.StartedAt
	s.mu.Unlock()

	// Announce the rename as the old name leaving and the new name
	// appearing, in that order, so a consumer's view transitions cleanly
	// rather than momentarily holding both names live. Without this, a
	// stream-only consumer keeps the old name forever as a frozen ghost and
	// never learns the new one exists — RenameAgent used to re-key its
	// internal maps in complete silence.
	//
	// The agentstore record is re-keyed by the caller (agent.Manager.Rename)
	// only *after* this returns, so it is still filed under oldName here —
	// Template/Repo/Branch/Model are left zero-valued rather than guessed,
	// matching spawnedAgentView's own degrade-gracefully contract.
	s.publish(observe.Event{
		Type:    observe.EventAgentStopped,
		Payload: &observe.AgentStoppedPayload{Agent: oldName},
	})
	s.publish(observe.Event{
		Type: observe.EventAgentSpawned,
		Payload: &observe.AgentSpawnedPayload{
			Agent: observe.Agent{
				Name:      newName,
				Status:    observe.StatusRunning,
				Restarts:  restarts,
				StartedAt: startedAt,
			},
		},
	})
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

// RunSupervisedOptions bundles RunSupervised's parameters. Grouped into a
// struct (rather than growing the positional-string signature further) once
// Version was added — see RunSupervised's doc comment.
type RunSupervisedOptions struct {
	ClaudePath string
	HomePath   string
	ConfigPath string
	// WebToken is the daemon's API bearer token, propagated to the
	// agent.Manager and the RestoreAgents path so restored/respawned agents
	// can authenticate against the daemon's web API.
	WebToken string
	// Version is the leo build version (internal/cli.Version, ldflags-
	// injected). Threaded through to web.WithVersion so GET /api/v1/state
	// can report Snapshot.LeoVersion. internal/cli owns Version and imports
	// internal/web, so it can't be read back from here — the CLI passes it
	// down explicitly instead. Empty is safe: the web layer just reports "".
	Version string
}

// RunSupervised starts the leo daemon: the web UI, cron scheduler, and the
// daemon IPC server, then restores + supervises ephemeral agents (each in
// its own tmux session with a restart loop). It no longer starts any
// config-declared "processes" — agents are the only supervised primitive.
func RunSupervised(opts RunSupervisedOptions) error {
	return supervisedExecFn(opts)
}

func defaultSupervisedExec(opts RunSupervisedOptions) error {
	claudePath, homePath, configPath, webToken := opts.ClaudePath, opts.HomePath, opts.ConfigPath, opts.WebToken

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

	// Ensure leo's dedicated tmux socket is backed by a foreground server
	// leo itself started (rather than tmux's own auto-daemonized one), so
	// agent sessions inherit macOS Local Network responsibility from the
	// signed leo binary. Must run before RestoreAgents/any supervise loop
	// issues a new-session. Fail-open: a daemonized fallback server is worse
	// than a dead daemon, so a failure here only warns.
	if err := tmux.EnsureForegroundServer(tmuxPath); err != nil {
		fmt.Fprintf(os.Stderr, "warning: foreground tmux server setup failed: %v\n", err)
	} else {
		go tmux.SuperviseForegroundServer(ctx, tmuxPath)
	}

	supervisor := NewSupervisor(ctx)
	supervisor.tmuxPath = tmuxPath
	supervisor.claudePath = claudePath
	supervisor.homePath = homePath
	supervisor.configPath = configPath

	bus, runLog, messageLog, activityTracker := wireObservability(ctx, supervisor, tmuxPath)

	// Start daemon IPC server with process state provider
	sockPath := filepath.Join(homePath, "state", "leo.sock")
	srv := daemon.New(sockPath, configPath, supervisor)
	// Threaded into web.New's extra Options by StartWeb — see
	// daemon.Server.SetObservability's doc comment.
	srv.SetObservability(bus, runLog, messageLog, activityTracker, opts.Version)
	// SetLogPath before Start/StartWeb so the Service page's log tail knows
	// where to read from — service is the only package that can compute
	// this path (LogPathFor) without an import cycle through daemon -> web.
	srv.SetLogPath(LogPathFor(homePath))
	// Wire the router's injector/aborter to the tmux path directly. Every
	// persistent-task delivery target is an agent tmux session now (Task 1-3
	// of the sessions->agents collapse); non-claude ephemeral agents don't
	// yet have a harness-aware injection dispatch table of their own, so
	// this stays the single fallback for every tmux session.
	srv.SetInjector(func(ctx context.Context, tmuxSession, prompt string) (*harness.Result, error) {
		return nil, tmux.InjectPrompt(context.Background(), tmuxPath, tmuxSession, prompt)
	})
	srv.SetAborter(func(tmuxSession string) error {
		return tmux.AbortPrompt(context.Background(), tmuxPath, tmuxSession)
	})
	if err := srv.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: daemon server failed to start: %v\n", err)
	} else {
		defer func() { _ = srv.Shutdown() }()
		fmt.Fprintf(os.Stdout, "daemon IPC server listening on %s\n", sockPath)

		// Build the agent.Manager shared by web, daemon, and CLI handlers.
		cfgLoader := func() (*config.Config, error) { return config.Load(configPath) }
		agentMgr := agent.New(cfgLoader, supervisor, tmuxPath, webToken)
		// runLog is the same Publisher wired into the supervisor (SetPublisher
		// above) — see agent.Manager.SetPublisher's doc comment for why the
		// manager needs its own seam: a stop/rename against a non-live agent
		// never reaches sup.StopAgent/RenameAgent, so it never reaches the
		// supervisor's own publish calls either.
		agentMgr.SetPublisher(runLog)
		srv.SetAgentManager(agentMgr)
		// The ensure-exists task-delivery path (config.ResolveTaskTarget +
		// runPersistent) needs the same agent.Manager to spawn/resume targets
		// before injection. agentMgr already satisfies daemon.EnsureAgentManager
		// (Live/Suspended/Resume/SpawnFromTemplate).
		srv.SetEnsurer(daemon.NewAgentEnsurer(agentMgr))

		// Idle-suspend sweep: suspends ephemeral agents that have gone idle
		// past their configured interval (see Manager.Suspend). Runs for the
		// daemon's lifetime; ctx cancellation stops it.
		go runIdleSweep(ctx, supervisor, agentMgr, tmuxPath, homePath)

		// Start web UI if enabled
		if cfg, err := config.Load(configPath); err == nil {
			if err := srv.StartWeb(cfg, agentMgr); err != nil {
				fmt.Fprintf(os.Stderr, "warning: web UI failed to start: %v\n", err)
			}
		}
	}

	// Restore ephemeral agents from previous run
	restored := RestoreAgents(homePath, tmuxPath, webToken, supervisor)
	if restored > 0 {
		fmt.Fprintf(os.Stdout, "restored %d ephemeral agent(s)\n", restored)
	}

	// opencode has no per-invocation system-prompt flag, so Leo's nudge is
	// delivered via opencode's global AGENTS.md instead. Only touch that
	// file (which lives outside any leo-managed directory) when opencode is
	// actually configured somewhere in this config.
	if cfg, err := config.Load(configPath); err == nil && cfg.UsesHarness("opencode") {
		if err := leomcp.EnsureOpenCodeContext(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "warning: opencode global AGENTS.md refresh failed: %v\n", err)
		}
	}

	// Block until shutdown is signalled. Ephemeral agents (restored above or
	// spawned later via SpawnAgent) run their own goroutines against ctx and
	// clean themselves up on cancellation, so this call simply blocks until
	// shutdown.
	<-ctx.Done()
	return nil
}

// wireObservability builds the observability event bus, run log, and
// activity tracker, wires the run log as the Publisher for the supervisor,
// and starts the tracker's sweep loop in a goroutine tied to ctx (so it
// exits at shutdown rather than leaking). Extracted from
// defaultSupervisedExec so this wiring — the thing most likely to silently
// regress (an Option never passed, a goroutine never started) — is directly
// callable from a test without booting the whole daemon.
//
// internal/run's producers are NOT wired here: every task execution is a
// `leo run` subprocess, a different OS process from the daemon, so a
// run.SetPublisher call made in this process could never be observed by
// run.publishEvent in that one. That producer instead reports over the IPC
// socket via daemon.ObservePublisher, wired per-invocation in
// internal/cli/run.go — see handleObserveTaskRun in internal/daemon/server.go
// for the daemon-side end of that path.
func wireObservability(ctx context.Context, sv *Supervisor, tmuxPath string) (*observe.Bus, *observe.RunLog, *observe.MessageLog, *observe.Tracker) {
	// runLog forwards every event to bus (the HTTP layer's read seam) and
	// additionally records task runs, so it is the single Publisher the
	// supervisor is given — see internal/observe.RunLog's doc comment for why
	// it wraps the bus rather than subscribing to it.
	bus := observe.NewBus()
	runLog := observe.NewRunLog(bus, 0)
	// messageLog wraps runLog for the same reason runLog wraps bus: recording
	// must be synchronous with publish, since a dropped subscriber would make
	// the snapshot's recent_messages silently incomplete. The web layer
	// publishes agent-to-agent messages through it; the supervisor keeps
	// publishing through runLog, which no message event ever reaches.
	messageLog := observe.NewMessageLog(runLog, 0)
	sv.SetPublisher(runLog)

	// sv.SessionNames is the narrow accessor onto the live agent-name ->
	// tmux-session-name mapping the tracker sweeps (see its doc comment for
	// why it isn't recomputed here).
	tracker := observe.NewTracker(tmuxPath, sv.SessionNames, runLog)
	go tracker.Start(ctx)

	return bus, runLog, messageLog, tracker
}

// driverFor resolves a spec's session driver. Empty harness means claude
// (records/state written before the field existed).
func driverFor(harnessName string) harness.SessionDriver {
	if harnessName == "" {
		harnessName = "claude"
	}
	h, err := harness.Get(harnessName)
	if err != nil {
		return nil // unreachable for validated configs; callers nil-check
	}
	return h.Driver()
}

// handleForSpec builds the SessionHandle superviseProcess hands to drivers.
func handleForSpec(spec ProcessSpec, id *procIdentity, homePath string) harness.SessionHandle {
	kind := spec.Kind
	if kind == "" {
		kind = harness.KindAgent
	}
	return harness.SessionHandle{
		Kind:          kind,
		Name:          id.Name(),
		TmuxSession:   id.SessionName(),
		Workspace:     spec.WorkDir,
		HomePath:      homePath,
		Env:           spec.Env,
		OpeningPrompt: spec.OpeningPrompt,
		IDs:           agentOrProcessIDs(homePath, id.Name()),
	}
}

// superviseProcess runs a single process in a tmux session with restart loop.
// Every registered harness (claude, codex, opencode) is DriveTmux today — a
// resident TUI supervised in a leo tmux session — so this loop is the single
// supervise path for every spec regardless of harness. Harness-specific
// behavior (workspace trust, resume-argv rewriting, dialog dismissal,
// quick-exit recovery) is expressed entirely through the driver's optional
// capability interfaces (PreLauncher, SessionArgsRefresher, PaneCare,
// QuickExitRecovery), asserted below; claude's driver leaves the
// PreLaunch/RefreshArgs hooks nil, so both no-op — it does use
// PaneCare/QuickExitRecovery.
func superviseProcess(ctx context.Context, tmuxPath, claudePath string, spec ProcessSpec, homePath string, sv *Supervisor, id *procIdentity) {
	sv.initState(spec.Name)

	// Resolve the driver once per supervise loop. Error is impossible for the
	// compiled-in claude adapter; on the defensive path drv stays nil and every
	// type assertion below (PaneCare, QuickExitRecovery, PreLauncher,
	// SessionArgsRefresher) simply misses, which degrades to "skip
	// dismissal" / "default quick-exit clear" / "no pre-launch hook" / "argv
	// unchanged" — the historical pre-driver behavior minus dialog
	// auto-dismissal.
	drv := driverFor(spec.Harness)

	harnessName := spec.Harness
	if harnessName == "" {
		harnessName = "claude"
	}
	binPath := harnessBinaryPath(harnessName, claudePath)

	var paneKey func(string) string
	if care, ok := drv.(harness.PaneCare); ok {
		paneKey = care.PaneKey
	}

	backoff := initialBackoff
	// adopt is a one-shot: honored only on the first iteration, and only when
	// the session actually survived the daemon bounce. Any in-loop restart
	// below always spawns a fresh session.
	adopt := spec.Adopt
	// openingPrompt is one-shot across restart iterations: delivered by the
	// driver's Start on the first successful launch (create or adopt), then
	// cleared so an in-loop restart never replays it.
	openingPrompt := spec.OpeningPrompt

	for {
		// Snapshot identity for this iteration. The tmux session name is also
		// re-read live by waitForSessionEnd (via id) so a rename mid-wait is
		// absorbed; the snapshot here governs this iteration's kill/new-session
		// and name-keyed state files.
		name := id.Name()
		sessionName := id.SessionName()
		currentArgs := id.Args()

		sv.setState(name, "running")

		// Clear any prior exit.code so a shell SIGKILL mid-run doesn't leave
		// the previous iteration's code on disk to be misattributed here.
		if spec.StateDir != "" {
			resetExitCode(spec.StateDir, name)
		}

		doAdopt := adopt && tmuxHasSession(tmuxPath, sessionName)
		adopt = false

		startTime := time.Now()

		if doAdopt {
			// Re-attach to the session that outlived the previous daemon
			// instead of recreating it. The running claude keeps its
			// conversation and in-flight work untouched — a daemon restart
			// (`leo update`, `leo service restart`) becomes a no-op for the
			// agent. If this session later ends, the loop falls through to a
			// normal fresh spawn (adopt is already cleared).
			fmt.Fprintf(os.Stdout, "[%s] adopted existing tmux session '%s', claude already running\n", name, sessionName)
		} else {
			if pl, ok := drv.(harness.PreLauncher); ok {
				if err := pl.PreLaunch(handleForSpec(spec, id, homePath)); err != nil {
					fmt.Fprintf(os.Stderr, "[%s] pre-launch: %v\n", name, err)
				}
			}
			if rf, ok := drv.(harness.SessionArgsRefresher); ok {
				currentArgs = rf.RefreshSessionArgs(currentArgs, agentOrProcessIDs(homePath, name).Get())
				id.setArgs(currentArgs)
			}

			claudeCmd := buildClaudeShellCmd(binPath, currentArgs, spec, os.Getenv("PATH"))
			// Env rides as `-e KEY=VALUE` argv, never inside claudeCmd: tmux
			// persists a pane's start command, so an interpolated credential
			// stays readable for the life of the session. See sessionEnvArgs.
			envArgs := sessionEnvArgs(tmuxPath, spec, os.Stderr)

			// Kill any stale tmux session with our name
			exec.Command(tmuxPath, tmux.Args("kill-session", "-t", tmux.Target(sessionName))...).Run()

			// Create a detached tmux session running claude
			newSessionArgs := []string{
				"new-session", "-d", "-s", sessionName,
				"-c", spec.WorkDir,
				"-x", "200", "-y", "50",
			}
			newSessionArgs = append(newSessionArgs, envArgs...)
			newSessionArgs = append(newSessionArgs, claudeCmd)
			createCmd := exec.CommandContext(ctx, tmuxPath, tmux.Args(newSessionArgs...)...)
			createCmd.Dir = spec.WorkDir
			createCmd.Env = os.Environ()
			// Surface tmux's own diagnostics. Without this a spawn failure
			// logs only "exit status 1" on every backoff — which is exactly
			// what an unsupported `-e` on tmux < 3.2 looks like. The argv
			// carries credentials; tmux's stderr does not.
			createCmd.Stderr = os.Stderr

			if err := createCmd.Run(); err != nil {
				sv.setState(name, "restarting")
				fmt.Fprintf(os.Stderr, "[%s] tmux new-session failed: %v, retrying in %s\n", name, err, backoff)
				select {
				case <-ctx.Done():
					sv.setState(name, "stopped")
					return
				case <-time.After(backoff):
				}
				backoff = time.Duration(math.Min(float64(backoff)*2, float64(maxBackoff)))
				continue
			}

			fmt.Fprintf(os.Stdout, "[%s] tmux session '%s' created, claude running\n", name, sessionName)

			// If any --dangerously-load-development-channels flags are present,
			// claude will show an interactive confirmation prompt on a fresh
			// start. Dismiss it by sending Enter (the default-highlighted
			// option is "I am using this for local development"). Runs in a
			// goroutine so the restart loop isn't blocked if the prompt never
			// appears. Skipped for an adopted session — that claude is already
			// running past the prompt.
			if hasDevChannelFlag(currentArgs) {
				fmt.Fprintf(os.Stdout, "[%s] auto-accepting dev-channel prompt\n", name)
				go func(sess string) {
					if err := tmux.AcceptDevChannelPrompt(ctx, tmuxPath, sess); err != nil && ctx.Err() == nil {
						fmt.Fprintf(os.Stderr, "[%s] warning: dev-channel auto-accept failed: %v\n", name, err)
					}
				}(sessionName)
			}
		}

		// Give the driver a chance to do whatever it needs before the first
		// Inject (e.g. delivering an opening prompt). Runs in a goroutine so
		// a slow/blocked Start never stalls the restart loop; claude's Start
		// is a no-op so this is behavior-neutral for every existing config.
		// The opening prompt is one-shot: cleared after the first launch
		// (create or adopt) so an in-loop restart never replays it.
		if drv != nil {
			startHandle := handleForSpec(spec, id, homePath)
			startHandle.OpeningPrompt = openingPrompt
			openingPrompt = ""
			go func(h harness.SessionHandle) {
				if err := drv.Start(ctx, h); err != nil && ctx.Err() == nil {
					fmt.Fprintf(os.Stderr, "[%s] driver start: %v\n", h.Name, err)
				}
			}(startHandle)
		}

		if waitForSessionEnd(ctx, tmuxPath, id, spec, startTime, paneKey) {
			sv.setState(name, "stopped")
			return
		}

		elapsed := time.Since(startTime)

		// Check if we were signaled to stop
		select {
		case <-ctx.Done():
			sv.setState(name, "stopped")
			return
		default:
		}

		sv.setState(name, "restarting")
		sv.incrementRestarts(name)

		// A very-quick exit means this iteration's session-selection flag is
		// unusable. Degrade in two steps so we recover the conversation when
		// we can and only fall back to a fresh session when resuming itself
		// fails:
		//
		//   1. --session-id <id> → --resume <id>
		//      claude refuses `--session-id` when that id's jsonl already
		//      exists ("Session ID ... is already in use") — which happens
		//      whenever we re-spawn a session whose transcript was already
		//      written (e.g. the tmux session was killed out from under us).
		//      Resuming rehydrates the existing conversation instead of
		//      crash-looping on a taken id.
		//   2. --resume <id> → fresh
		//      Resume itself quick-exited, so treat the jsonl as poisoned:
		//      strip it, clear the stored session, and mark the agent
		//      NoResume so a daemon restart won't reintroduce --resume via
		//      RestoreAgents. No-op for non-agent specs (no matching record).
		if elapsed < quickExitThreshold {
			newArgs, action := recoverQuickExit(drv, currentArgs)
			switch action {
			case harness.QuickExitRetryArgs:
				currentArgs = newArgs
				id.setArgs(currentArgs)
				fmt.Fprintf(os.Stderr, "[%s] %s exited quickly (%.0fs), retrying with --resume\n", name, harnessName, elapsed.Seconds())
			case harness.QuickExitClearAndNoResume:
				currentArgs = newArgs
				id.setArgs(currentArgs)
				clearProcessSession(homePath, name)
				markAgentNoResume(homePath, name)
				fmt.Fprintf(os.Stderr, "[%s] %s exited quickly (%.0fs), cleared stale session\n", name, harnessName, elapsed.Seconds())
			case harness.QuickExitClearSession:
				clearProcessSession(homePath, name)
				fmt.Fprintf(os.Stderr, "[%s] %s exited quickly (%.0fs)\n", name, harnessName, elapsed.Seconds())
			case harness.QuickExitNone:
				fmt.Fprintf(os.Stderr, "[%s] %s exited quickly (%.0fs)\n", name, harnessName, elapsed.Seconds())
			}
		}

		// Read exit info written by the shell wrapper, compose the per-process
		// post-mortem, and emit the new log line. All of this is best-effort —
		// when StateDir is empty or the shell didn't finish writing (e.g.
		// SIGKILL to tmux), we still log what we can.
		exitCode, codeOK := 0, false
		signal := "none"
		var tail []string
		if spec.StateDir != "" {
			exitCode, codeOK = readExitCode(spec.StateDir, name)
			if codeOK {
				signal = decodeSignal(exitCode)
			}
			tail = tailLines(processStderrPath(spec.StateDir, name), exitStderrTailLines)
			_ = writeExitLog(spec.StateDir, name, exitCode, codeOK, signal, elapsed, tail)
		}
		logProcessExit(os.Stderr, name, elapsed, backoff, exitCode, codeOK, signal,
			processExitLogPath(spec.StateDir, name), len(tail) > 0)

		// Sleep the current backoff, then advance for the NEXT iteration.
		// `backoff` starts at initialBackoff on cold start; a run that lasts
		// >= healthyUptimeThreshold resets it, anything shorter doubles it
		// up to maxBackoff. The <15s quick-exit path above also strips
		// --resume but doesn't change backoff — it's purely a session fix.
		select {
		case <-ctx.Done():
			sv.setState(name, "stopped")
			return
		case <-time.After(backoff):
		}
		backoff = advanceBackoff(backoff, elapsed)
	}
}

// waitForSessionEnd blocks until the tmux session ends or the context is cancelled.
// Returns true if the context was cancelled (should stop).
//
// paneKey decides how to clear a blocking startup/announcement dialog seen in
// a captured pane (see harness.PaneCare). nil means the driver has no pane
// care to offer, so dismissal is skipped entirely.
func waitForSessionEnd(ctx context.Context, tmuxPath string, id *procIdentity, spec ProcessSpec, startTime time.Time, paneKey func(string) string) bool {
	_ = spec      // kept in signature for future lifecycle hooks
	_ = startTime // kept in signature for future lifecycle hooks
	for {
		select {
		case <-ctx.Done():
			exec.Command(tmuxPath, tmux.Args("kill-session", "-t", tmux.Target(id.SessionName()))...).Run()
			return true
		case <-time.After(sessionPollInterval):
		}

		// Re-read the session name each poll so a live rename is followed
		// rather than reported as a vanished session.
		sessionName := id.SessionName()
		if !tmuxHasSession(tmuxPath, sessionName) {
			return false
		}

		// Auto-dismiss the "Resume from summary" prompt that blocks
		// unattended sessions when they exceed the context threshold, plus any
		// other blocking startup/announcement dialog, per the driver's policy.
		if paneKey != nil {
			dismissStartupDialog(tmuxPath, sessionName, id.Name(), paneKey)
		}
	}
}

// dismissStartupDialog captures the session's recent pane and clears a blocking
// startup/announcement dialog so message injection isn't stuck behind it. See
// paneKey (harness.PaneCare.PaneKey) for the policy. Best-effort: capture/send
// failures are ignored and retried on the next poll.
func dismissStartupDialog(tmuxPath, sessionName, processName string, paneKey func(string) string) {
	target := tmux.ResolvePaneOrFallback(context.Background(), tmuxPath, sessionName)
	out, err := exec.Command(tmuxPath, tmux.Args("capture-pane", "-t", target, "-p", "-S", "-10")...).Output()
	if err != nil {
		return
	}
	key := paneKey(string(out))
	if key == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "[%s] dismissing startup dialog with %s\n", processName, key)
	exec.Command(tmuxPath, tmux.Args("send-keys", "-t", target, key)...).Run() //nolint:errcheck
}

// recoverQuickExit consults the driver's ladder when it has one; the default
// mirrors the historical behavior (clear the stored session, keep args).
func recoverQuickExit(drv harness.SessionDriver, args []string) ([]string, harness.QuickExitAction) {
	if r, ok := drv.(harness.QuickExitRecovery); ok {
		return r.RecoverQuickExit(args)
	}
	return args, harness.QuickExitClearSession
}

func findTmux() (string, error) {
	return tmux.Locate()
}

// markAgentNoResume sets NoResume=true and clears SessionID on the agentstore
// record matching name. The flag tells RestoreAgents to skip --resume on the
// next daemon restart so a poisoned session jsonl isn't re-rehydrated. It's a
// no-op when no agent record exists for this name (i.e. the spec is a regular
// supervised process, not an ephemeral agent).
func markAgentNoResume(homePath, name string) {
	path := agentstore.FilePath(homePath)
	records, err := agentstore.Load(path)
	if err != nil || len(records) == 0 {
		return
	}
	rec, ok := records[name]
	if !ok {
		return
	}
	if rec.NoResume && rec.SessionID == "" {
		return
	}
	rec.NoResume = true
	rec.SessionID = ""
	if err := agentstore.Save(homePath, rec); err != nil {
		fmt.Fprintf(os.Stderr, "[%s] warning: could not mark agent no-resume: %v\n", name, err)
	}
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

// supervisorWebTokenPattern matches the exact shape web.EnsureAPIToken writes:
// 32 random bytes hex-encoded, i.e. 64 hex characters. Defense-in-depth so a
// malformed token can never introduce shell tokens; shellQuote already makes
// that impossible, but the pattern also catches accidental corruption
// upstream.
var supervisorWebTokenPattern = regexp.MustCompile(`^[A-Fa-f0-9]{64}$`)

// buildClaudeShellCmd assembles the shell command string that tmux runs
// to launch claude. Defense-in-depth: every interpolated env key is
// validated against supervisorEnvKeyPattern, and spec.WebPort is
// validated against supervisorWebPortPattern, before being embedded in
// the resulting shell string. Invalid entries are dropped and a warning
// is written to warnOut.
//
// Values are shell-quoted via shellQuote (single-quote safe), so they
// cannot introduce new shell tokens even if they contain metacharacters.
// harnessBinaryPath resolves the executable the supervise loop should exec
// for a spec's harness. Claude (and the empty value on pre-field records)
// keeps the supervisor's already-resolved claudePath. Any other registered
// harness resolves its own binary — absolute via LookPath when the daemon's
// PATH can find it (so the pane doesn't depend on PATH order), bare
// otherwise so the pane's exported PATH still gets a chance. An unregistered
// name falls back to claudePath: config validation rejects those long before
// a spec reaches the supervisor, and the pane failure is loud either way.
func harnessBinaryPath(harnessName, claudePath string) string {
	if harnessName == "" || harnessName == "claude" {
		return claudePath
	}
	h, err := harness.Get(harnessName)
	if err != nil {
		return claudePath
	}
	if abs, err := exec.LookPath(h.Binary()); err == nil {
		return abs
	}
	return h.Binary()
}

// sessionEnvArgs returns the `-e KEY=VALUE` args for tmux new-session,
// carrying the process's own configured env plus leo's control vars.
//
// PATH is deliberately absent: tmux ignores `-e PATH=…` entirely (verified on
// tmux 3.6a, with and without a pre-existing server), so listing it here
// would assert a guarantee tmux does not honour. PATH is exported inline in
// the pane command instead — see buildClaudeShellCmd.
//
// Env travels as argv rather than as `export K=V;` inside the shell command
// on purpose. tmux keeps a pane's start command for the life of the session
// (`list-panes -F '#{pane_start_command}'`, and it shows in ps), so anything
// interpolated there is readable for hours by every process running as the
// same user — including a 1Password service-account token. As new-session
// argv the value is exposed only in the short-lived tmux client's own argv:
// hours of exposure become milliseconds.
//
// This is exposure reduction, not isolation: `tmux show-environment` still
// reports session env to anyone who can reach the socket, and leo.yaml is
// readable by every same-uid agent anyway. It removes the copy that leaks
// incidentally.
//
// Requires tmux 3.2+ (`-e` on new-session), matching leo's documented tmux
// baseline. Keys are validated; leo's own vars win on collision, preserving
// the precedence of the old export ordering.
func sessionEnvArgs(tmuxPath string, spec ProcessSpec, warnOut io.Writer) []string {
	env := make(map[string]string, len(spec.Env)+4)

	for k, v := range spec.Env {
		if !supervisorEnvKeyPattern.MatchString(k) {
			if warnOut != nil {
				fmt.Fprintf(warnOut, "[%s] warning: dropping invalid env key %q\n", spec.Name, k)
			}
			continue
		}
		if k == "PATH" {
			// tmux ignores session-env PATH, and leo's own inline export runs
			// after it would anyway — as it did before this change too, so a
			// configured PATH has never taken effect. Say so rather than
			// letting it look applied.
			if warnOut != nil {
				fmt.Fprintf(warnOut, "[%s] warning: ignoring configured PATH — leo exports the daemon's PATH into the session\n", spec.Name)
			}
			continue
		}
		env[k] = v
	}

	env["LEO_PROCESS_NAME"] = spec.Name
	env["LEO_TMUX_PATH"] = tmuxPath
	if spec.WebPort != "" {
		if supervisorWebPortPattern.MatchString(spec.WebPort) {
			env["LEO_WEB_PORT"] = spec.WebPort
		} else if warnOut != nil {
			fmt.Fprintf(warnOut, "[%s] warning: dropping invalid LEO_WEB_PORT %q\n", spec.Name, spec.WebPort)
		}
	}
	if spec.WebToken != "" {
		if supervisorWebTokenPattern.MatchString(spec.WebToken) {
			env["LEO_API_TOKEN"] = spec.WebToken
		} else if warnOut != nil {
			// Do not echo the token itself; it is a secret even when malformed.
			fmt.Fprintf(warnOut, "[%s] warning: dropping malformed LEO_API_TOKEN\n", spec.Name)
		}
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	// Map iteration is randomized; sort so the args are stable across spawns.
	sort.Strings(keys)

	out := make([]string, 0, len(keys)*2)
	for _, k := range keys {
		out = append(out, "-e", k+"="+env[k])
	}
	return out
}

// buildClaudeShellCmd assembles the command tmux runs in the pane. It carries
// no env except PATH — see sessionEnvArgs for why the rest moved out, and the
// PATH export below for why that one stayed.
func buildClaudeShellCmd(claudePath string, args []string, spec ProcessSpec, pathEnv string) string {
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

	// PATH stays inline, unlike the rest of the env. tmux runs this command
	// through $SHELL -c, so the shell's rc files run first — zsh sources
	// ~/.zshenv even non-interactively — and a PATH set there overrides the
	// one the pane inherits from the new-session client. An inline export
	// runs after rc files and wins, which is how leo's PATH has always
	// reached agents. Keeping it here changes no security property: PATH is
	// not a credential, and it was never the thing leaking from
	// pane_start_command.
	if pathEnv != "" {
		cmd = fmt.Sprintf("export PATH=%s; %s", shellQuote(pathEnv), cmd)
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
