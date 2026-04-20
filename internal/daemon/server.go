package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/cron"
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
	Prune(ctx context.Context, name string, opts agent.PruneOptions) error
	List() []agent.Record
	Logs(name string, lines int) (string, error)
	SessionName(name string) string
	Resolve(query string) (agent.Record, error)
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
	}

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
	mux.HandleFunc("GET /process/list", s.handleProcessList)
	mux.HandleFunc("POST /config/reload", s.handleConfigReload)

	// Agent lifecycle — served only when an AgentManager has been attached via
	// SetAgentManager(). Handlers short-circuit with 503 when s.agentMgr is nil.
	mux.HandleFunc("POST /agents/spawn", s.handleAgentSpawn)
	mux.HandleFunc("GET /agents/list", s.handleAgentList)
	mux.HandleFunc("GET /agents/resolve", s.handleAgentResolve)
	mux.HandleFunc("POST /agents/{name}/stop", s.handleAgentStop)
	mux.HandleFunc("POST /agents/{name}/prune", s.handleAgentPrune)
	mux.HandleFunc("GET /agents/{name}/logs", s.handleAgentLogs)
	mux.HandleFunc("GET /agents/{name}/session", s.handleAgentSession)

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
		if err := s.httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
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

	port := cfg.WebPort()
	s.webServer = web.New(s.configPath, &processAdapter{inner: s.processes}, s.scheduler, s, agentSvc, web.Options{
		Port:         port,
		APIToken:     apiToken,
		AllowedHosts: cfg.Web.AllowedHosts,
	})
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := s.httpServer.Shutdown(ctx)
	// Always try to remove socket file
	os.Remove(s.sockPath)
	return err
}

// SockPath returns the path to the Unix socket.
func (s *Server) SockPath() string {
	return s.sockPath
}

// SetAgentManager attaches an agent manager. Must be called before any /agents/*
// request is served; otherwise those endpoints return 503.
func (s *Server) SetAgentManager(m AgentManager) {
	s.agentMgr = m
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Response{OK: true})
}

func (s *Server) handleProcessList(w http.ResponseWriter, r *http.Request) {
	if s.processes == nil {
		writeJSON(w, http.StatusOK, Response{OK: true, Data: json.RawMessage("{}")})
		return
	}
	states := s.processes.States()
	data, err := json.Marshal(states)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("marshaling process states: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, Response{OK: false, Error: msg})
}
