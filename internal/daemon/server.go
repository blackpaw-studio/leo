package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/observe"
	"github.com/blackpaw-studio/leo/internal/tmux"
	"github.com/blackpaw-studio/leo/internal/web"
)

// ProcessStateProvider returns the state of all supervised processes.
// This is implemented by service.Supervisor.
type ProcessStateProvider interface {
	States() map[string]ProcessStateInfo
}

// ProcessStateInfo is the daemon-facing view of a process state. Aliased to
// agent.ProcessState so the agent package, daemon, and service all agree on
// a single struct without import cycles.
type ProcessStateInfo = agent.ProcessState

// AgentManager is the interface daemon socket handlers use to drive the agent
// lifecycle. It is satisfied by *agent.Manager.
type AgentManager interface {
	Spawn(ctx context.Context, spec agent.SpawnSpec) (agent.Record, error)
	Stop(name string) error
	Suspend(name string) error
	Resume(name string) (agent.Record, error)
	Reset(name string) error
	Restart(name string) error
	RestartAll() agent.RestartResult
	Prune(ctx context.Context, name string, opts agent.PruneOptions) error
	List() []agent.Record
	Logs(name string, lines int) (string, error)
	SessionName(name string) string
	Resolve(query string) (agent.Record, error)
	Rename(query, newName string) (agent.Record, error)
	// ResolveHandle resolves an agent name to its harness name and the
	// SessionHandle a SessionDriver needs to act on it. ok=false means "not
	// an ephemeral agent" — callers fall back to the tmux/claude path.
	ResolveHandle(name string) (harnessName string, h harness.SessionHandle, ok bool)
}

// Server is an HTTP server listening on a Unix socket for daemon IPC.
type Server struct {
	sockPath   string
	configPath string
	httpServer *http.Server
	listener   net.Listener
	scheduler  *cron.Scheduler
	processes  ProcessStateProvider
	webServer  *web.Server
	agentMgr   AgentManager
	router     *sessionRouter
	logPath    string // service log path, set via SetLogPath; threaded into web.Options.LogPath by StartWeb
	// resolveHandle backs web.Options.ResolveHandle: resolves a config-defined
	// process name to its harness name and SessionHandle. Set via
	// SetResolveHandle by service boot; nil means every process is claude.
	resolveHandle func(name string) (harnessName string, h harness.SessionHandle, ok bool)

	// Observability dependencies, wired via SetObservability and threaded
	// into web.New's extra Options by StartWeb. All are optional (nil-safe on
	// the web side — see web.WithEventSource/WithActivityProvider/WithRunLog),
	// so a daemon that never calls SetObservability boots unchanged.
	observeBus      *observe.Bus
	observeRunLog   *observe.RunLog
	observeActivity observe.ActivityProvider
	leoVersion      string
}

// SetObservability wires the observability event bus, run log, activity
// tracker, and build version threaded into web.Options by StartWeb. Must be
// called before StartWeb. All parameters are optional (nil/empty is safe);
// service boot is the only caller today (see internal/service/process.go).
func (s *Server) SetObservability(bus *observe.Bus, runLog *observe.RunLog, activity observe.ActivityProvider, version string) {
	s.observeBus = bus
	s.observeRunLog = runLog
	s.observeActivity = activity
	s.leoVersion = version
}

// New creates a new daemon server. The processes provider is optional (may be nil).
func New(sockPath, configPath string, processes ProcessStateProvider) *Server {
	leoPath, err := exec.LookPath("leo")
	if err != nil {
		leoPath = "leo"
	}

	s := &Server{
		sockPath:   sockPath,
		configPath: configPath,
		scheduler:  cron.New(leoPath, configPath),
		processes:  processes,
		router:     newSessionRouter(),
	}

	// The injector is intentionally NOT wired here: deciding how to inject
	// (tmux for claude, a SessionDriver for everything else) requires
	// resolving per-session harness config, which lives in internal/service
	// (the daemon package must not import that decision to avoid pulling
	// config/harness resolution into the IPC layer). The caller that owns
	// process/session specs (service.defaultSupervisedExec) calls
	// SetInjector with a harness-aware closure after New() returns. Tests
	// that need a fake call SetInjector directly.
	s.router.SetAborter(func(tmuxSession string) error {
		return tmux.AbortPrompt(context.Background(), tmuxPath(), tmuxSession)
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /cron/install", s.handleCronInstall)
	mux.HandleFunc("POST /cron/remove", s.handleCronRemove)
	mux.HandleFunc("GET /cron/list", s.handleCronList)
	mux.HandleFunc("POST /task/add", s.handleTaskAdd)
	mux.HandleFunc("POST /task/remove", s.handleTaskRemove)
	mux.HandleFunc("POST /task/enable", s.handleTaskEnable)
	mux.HandleFunc("POST /task/disable", s.handleTaskDisable)
	mux.HandleFunc("GET /task/list", s.handleTaskList)
	mux.HandleFunc("POST /config/reload", s.handleConfigReload)

	// Persistent-task prompt delivery (ensure-exists into agent tmux sessions).
	mux.HandleFunc("POST /task/enqueue", s.handleTaskEnqueue)
	mux.HandleFunc("GET /task/await", s.handleTaskAwait)
	mux.HandleFunc("POST /task/report", s.handleTaskReport)

	// Agent lifecycle — served only when an AgentManager has been attached via
	// SetAgentManager(). Handlers short-circuit with 503 when s.agentMgr is nil.
	mux.HandleFunc("POST /agents/spawn", s.handleAgentSpawn)
	mux.HandleFunc("GET /agents/list", s.handleAgentList)
	mux.HandleFunc("GET /agents/resolve", s.handleAgentResolve)
	mux.HandleFunc("POST /agents/restart", s.handleAgentRestartAll)
	mux.HandleFunc("POST /agents/{name}/stop", s.handleAgentStop)
	mux.HandleFunc("POST /agents/{name}/suspend", s.handleAgentSuspend)
	mux.HandleFunc("POST /agents/{name}/resume", s.handleAgentResume)
	mux.HandleFunc("POST /agents/{name}/reset", s.handleAgentReset)
	mux.HandleFunc("POST /agents/{name}/restart", s.handleAgentRestart)
	mux.HandleFunc("POST /agents/{name}/prune", s.handleAgentPrune)
	mux.HandleFunc("POST /agents/{name}/rename", s.handleAgentRename)
	mux.HandleFunc("GET /agents/{name}/logs", s.handleAgentLogs)
	mux.HandleFunc("GET /agents/{name}/session", s.handleAgentSession)
	mux.HandleFunc("GET /agents/{name}/attach-spec", s.handleAgentAttachSpec)

	s.httpServer = &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	return s
}

// Start binds the Unix socket and begins serving requests.
func (s *Server) Start() error {
	// Remove stale socket if present. If removal fails, net.Listen below will
	// return a useful "address in use" error — but surface any non-ENOENT
	// error here so a permissions problem on the state dir is not masked.
	if _, err := os.Stat(s.sockPath); err == nil {
		if err := os.Remove(s.sockPath); err != nil {
			return fmt.Errorf("removing stale socket %s: %w", s.sockPath, err)
		}
	}

	// Bind the socket under a tight umask so it is created with mode 0600
	// from the start. Without this, net.Listen creates the socket under the
	// process umask (typically 0022 → 0644), leaving a brief window where
	// any local process can connect before the os.Chmod below tightens it.
	oldMask := syscall.Umask(0o077)
	ln, err := net.Listen("unix", s.sockPath)
	syscall.Umask(oldMask)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", s.sockPath, err)
	}

	// Belt and suspenders: chmod explicitly in case the filesystem ignored
	// the umask (some network filesystems do) or a future Go version changes
	// the socket-creation mode.
	if err := os.Chmod(s.sockPath, 0600); err != nil {
		_ = ln.Close()
		return fmt.Errorf("setting socket permissions: %w", err)
	}

	s.listener = ln

	// Auto-load schedules from config
	if cfg, err := config.Load(s.configPath); err == nil {
		if err := s.scheduler.Install(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to load cron schedules: %v\n", err)
		}
	}
	s.scheduler.Start()

	go func() {
		if err := s.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "daemon HTTP server error: %v\n", err)
		}
	}()

	return nil
}

// processAdapter wraps a daemon ProcessStateProvider to satisfy web.ProcessStateProvider.
type processAdapter struct {
	inner ProcessStateProvider
}

func (a *processAdapter) States() map[string]web.ProcessStateInfo {
	if a.inner == nil {
		return nil
	}
	states := a.inner.States()
	result := make(map[string]web.ProcessStateInfo, len(states))
	for k, v := range states {
		result[k] = web.ProcessStateInfo{
			Name:      v.Name,
			Status:    v.Status,
			StartedAt: v.StartedAt,
			Restarts:  v.Restarts,
			Ephemeral: v.Ephemeral,
		}
	}
	return result
}

// AgentSpawnSpec is retained as an alias to agent.SpawnRequest for backwards
// compatibility with call sites; new code should use agent.SpawnRequest directly.
type AgentSpawnSpec = agent.SpawnRequest

// StartWeb starts the web UI on a TCP listener if web is enabled in config.
// agentSvc is the high-level agent.Manager used by web and daemon handlers; it
// may be nil to disable agent UI features.
//
// Before serving any request the web package mints (or loads) a bearer token
// at <state>/api.token, used to gate /api/* routes. The file is user-only
// (0600) and readable by plugins running as the same Unix user.
func (s *Server) StartWeb(cfg *config.Config, agentSvc web.AgentService) error {
	if !cfg.Web.Enabled {
		return nil
	}

	apiToken, err := web.EnsureAPIToken(cfg.StatePath())
	if err != nil {
		return fmt.Errorf("preparing web api token: %w", err)
	}
	// Agents get their own, less privileged token — see web.EnsureAgentToken.
	agentToken, err := web.EnsureAgentToken(cfg.StatePath())
	if err != nil {
		return fmt.Errorf("preparing agent api token: %w", err)
	}

	port := cfg.WebPort()
	var observeOpts []web.Option
	if s.observeBus != nil {
		observeOpts = append(observeOpts, web.WithEventSource(s.observeBus))
	}
	if s.observeRunLog != nil {
		observeOpts = append(observeOpts, web.WithRunLog(s.observeRunLog))
	}
	if s.observeActivity != nil {
		observeOpts = append(observeOpts, web.WithActivityProvider(s.observeActivity))
	}
	if s.leoVersion != "" {
		observeOpts = append(observeOpts, web.WithVersion(s.leoVersion))
	}
	s.webServer = web.New(s.configPath, &processAdapter{inner: s.processes}, s.scheduler, s, agentSvc, web.Options{
		Port:          port,
		APIToken:      apiToken,
		AgentToken:    agentToken,
		AllowedHosts:  cfg.Web.AllowedHosts,
		LogPath:       s.logPath,
		ResolveHandle: s.resolveHandle,
		// Consults record to <state>/consults for `leo consult watch`.
		ConsultRecorder: consult.NewFileRecorder(cfg.StatePath()),
	}, observeOpts...)
	bind := cfg.WebBind()
	addr := fmt.Sprintf("%s:%d", bind, port)
	if err := s.webServer.ListenAndServe(addr); err != nil {
		return fmt.Errorf("starting web UI: %w", err)
	}
	fmt.Fprintf(os.Stderr, "web UI listening on http://%s\n", addr)
	fmt.Fprintf(os.Stderr, "api token stored at %s (used for /api/* Bearer auth)\n", web.APITokenPath(cfg.StatePath()))
	if !config.IsLoopbackBind(bind) {
		fmt.Fprintf(os.Stderr,
			"WARNING: web.bind=%q exposes the Leo web UI beyond localhost. "+
				"The UI uses Host/Origin pinning for browser routes and a bearer "+
				"token for /api/*, but is still intended for single-user use. "+
				"Only expose on trusted networks.\n",
			bind)
	}
	return nil
}

// ReloadConfig reloads config from disk and re-syncs the scheduler.
// Implements web.ConfigReloader.
func (s *Server) ReloadConfig() error {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	return s.scheduler.Install(cfg)
}

// Shutdown gracefully stops the server and removes the socket file.
func (s *Server) Shutdown() error {
	s.scheduler.Stop()

	if s.webServer != nil {
		s.webServer.Shutdown() //nolint:errcheck
	}

	// Stop the router so its pump and janitor goroutines exit.
	if s.router != nil {
		s.router.Stop()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := s.httpServer.Shutdown(ctx)
	// Always try to remove socket file
	os.Remove(s.sockPath)
	return err
}

// SockPath returns the path to the Unix socket.
// SetInjector wires the session router's prompt-injection function. Must be
// called before StartPump for any session — service boot calls this with a
// harness-aware closure once (see internal/service/process.go); tests call
// it with a fake.
func (s *Server) SetInjector(fn func(ctx context.Context, session, prompt string) (*harness.Result, error)) {
	s.router.SetInjector(fn)
}

// SetAborter overrides the session router's abort function. Tests pair this
// with SetInjector to fully bypass tmux.
func (s *Server) SetAborter(fn func(session string) error) {
	s.router.SetAborter(fn)
}

func (s *Server) SockPath() string {
	return s.sockPath
}

// SetAgentManager attaches an agent manager. Must be called before any /agents/*
// request is served; otherwise those endpoints return 503.
func (s *Server) SetAgentManager(m AgentManager) {
	s.agentMgr = m
}

// SetEnsurer wires the session router's AgentEnsurer, used by the
// ensure-exists task-delivery path (spawn/resume an agent target before
// injecting) for persistent tasks routed via config.ResolveTaskTarget.
// Optional: leaving it unset is safe — invocations without an EnsureSpec
// never consult it.
func (s *Server) SetEnsurer(e AgentEnsurer) {
	s.router.SetEnsurer(e)
}

// SetResolveHandle wires the process-side handle resolver threaded into
// web.Options.ResolveHandle by StartWeb. Optional; if never called, every
// process is treated as claude (today's behavior). Service boot calls this
// with a closure over its supervisor + []ProcessSpec before StartWeb.
func (s *Server) SetResolveHandle(fn func(name string) (harnessName string, h harness.SessionHandle, ok bool)) {
	s.resolveHandle = fn
}

// SetLogPath records the service log path for the web UI's log tail. Must be
// called before StartWeb for the Service page's log viewer to work.
// internal/daemon cannot compute this itself (service.LogPathFor lives in
// internal/service, which imports internal/daemon — importing it back here
// would cycle), so the caller that owns homePath (internal/service/process.go)
// passes the already-computed path through.
func (s *Server) SetLogPath(path string) {
	s.logPath = path
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Response{OK: true})
}

type taskEnqueueReq struct {
	InvocationID   string   `json:"invocation_id,omitempty"`
	Session        string   `json:"session"`
	TmuxSession    string   `json:"tmux_session,omitempty"`
	Task           string   `json:"task"`
	Prompt         string   `json:"prompt"`
	Channels       []string `json:"channels"`
	QueueMax       int      `json:"queue_max"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	// Ensure, when present, is the ensure-exists spec for the agent-routed
	// persistent-task path (see EnqueueParams.Ensure).
	Ensure *EnsureSpec `json:"ensure,omitempty"`
}

type taskEnqueueResp struct {
	Accepted     bool   `json:"accepted"`
	InvocationID string `json:"invocation_id,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

func (s *Server) handleTaskEnqueue(w http.ResponseWriter, r *http.Request) {
	var req taskEnqueueReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if req.Session == "" || req.Task == "" || req.Prompt == "" {
		writeError(w, http.StatusBadRequest, "session, task, prompt are required")
		return
	}
	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	// Default the tmux target to the logical session name for older clients
	// that don't send tmux_session, preserving prior behavior for them.
	tmuxSession := req.TmuxSession
	if tmuxSession == "" {
		tmuxSession = req.Session
	}
	inv, ok := s.router.EnqueueWithID(req.InvocationID, EnqueueParams{
		Session:     req.Session,
		TmuxSession: tmuxSession,
		Task:        req.Task,
		Prompt:      req.Prompt,
		Channels:    req.Channels,
		QueueMax:    req.QueueMax,
		Timeout:     timeout,
		Ensure:      req.Ensure,
	})
	if !ok {
		writeJSON(w, http.StatusOK, taskEnqueueResp{Accepted: false, Reason: "queue full"})
		return
	}
	s.router.StartPump(req.Session)
	writeJSON(w, http.StatusOK, taskEnqueueResp{Accepted: true, InvocationID: inv.ID})
}

func (s *Server) handleTaskAwait(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("invocation_id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "invocation_id required")
		return
	}
	inv, ok := s.router.Lookup(id)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown invocation_id")
		return
	}
	// Long-poll handler; disable the server's WriteTimeout for this connection.
	if rc := http.NewResponseController(w); rc != nil {
		_ = rc.SetWriteDeadline(time.Time{})
	}
	select {
	case res := <-inv.Result:
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":            res.OK,
			"session_id":    res.SessionID,
			"final_message": res.FinalMessage,
			"error":         res.Err,
		})
	case <-r.Context().Done():
		writeError(w, http.StatusGatewayTimeout, "request cancelled")
	}
}

type taskReportReq struct {
	InvocationID string `json:"invocation_id"`
	SessionID    string `json:"session_id"`
	FinalMessage string `json:"final_message"`
}

func (s *Server) handleTaskReport(w http.ResponseWriter, r *http.Request) {
	var req taskReportReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if req.InvocationID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true}) // human turn — ignore
		return
	}
	s.router.Report(req.InvocationID, InvocationResult{
		OK:           true,
		SessionID:    req.SessionID,
		FinalMessage: req.FinalMessage,
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, Response{OK: false, Error: msg})
}

// tmuxPath resolves the tmux binary path, falling back to "tmux" on the PATH
// when LookPath fails. Resolved lazily on each call so PATH changes in tests
// are observable.
func tmuxPath() string {
	p, err := exec.LookPath("tmux")
	if err != nil {
		return "tmux"
	}
	return p
}
