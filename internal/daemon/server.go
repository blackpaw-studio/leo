package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/chat"
	"github.com/blackpaw-studio/leo/internal/clienttools"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/devices"
	"github.com/blackpaw-studio/leo/internal/drain"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/harness/claude/bridgemod"
	"github.com/blackpaw-studio/leo/internal/leomcp"
	"github.com/blackpaw-studio/leo/internal/observe"
	"github.com/blackpaw-studio/leo/internal/observe/httpapi"
	"github.com/blackpaw-studio/leo/internal/startup"
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
	Stop(name string, opts agent.StopOptions) error
	// Start clears a dormant agent's Stopped/StoppedReason/WakeOnMessage flags
	// and respawns it, resuming its prior claude session.
	Start(name string) error
	Reset(name string) error
	Restart(name string) error
	// SwitchTemplate re-points an agent at a different template, swapping its
	// wiring and which per-template conversation is live.
	SwitchTemplate(name, template string) (agent.SwitchResult, error)
	// SetEnvironments changes an agent's named environments, restarting it
	// (resuming its session) when live.
	SetEnvironments(name string, names []string) (agent.SetEnvironmentsResult, error)
	RestartAll() agent.RestartResult
	// StaleAgents reports running agents whose wiring would change if they
	// were restarted — what `leo update` offers to bounce after a binary swap.
	StaleAgents() []agent.StaleAgent
	// Delete removes the agentstore record for name — plus its worktree and
	// branch when it has one. Refuses a live agent.
	Delete(ctx context.Context, name string, opts agent.DeleteOptions) error
	// DeletePlan resolves name and reports what Delete would remove, without
	// removing anything — the shared seam CLI/picker/web format their delete
	// confirmation from.
	DeletePlan(name string) (agent.DeletePlan, error)
	List() []agent.Record
	Logs(name string, lines int) (string, error)
	SessionName(name string) string
	Resolve(query string) (agent.Record, error)
	Rename(query, newName string) (agent.Record, error)
	// ResolveHandle resolves an agent name to its harness name and the
	// SessionHandle a SessionDriver needs to act on it. ok=false means "not
	// an ephemeral agent" — callers fall back to the tmux/claude path.
	ResolveHandle(name string) (harnessName string, h harness.SessionHandle, ok bool)
	// RepublishAppearance publishes every agent's resolved appearance
	// against current config (agent_appearance_changed).
	RepublishAppearance()
	// SetAppearance patches an agent's manual emoji and color and returns
	// its resolved appearance.
	SetAppearance(name string, patch agentstore.AppearancePatch) (*observe.Appearance, error)
}

// Server is an HTTP server listening on a Unix socket for daemon IPC.
type Server struct {
	// configWriter serializes every in-process leo.yaml write: the IPC task
	// handlers here and the web server, which is handed the same Writer.
	configWriter *config.Writer
	sockPath     string
	configPath   string
	httpServer   *http.Server
	// endStreams cancels every request context (http.Server.BaseContext), so
	// long-lived handlers (event streams, long polls) return as soon as
	// shutdown begins instead of holding httpServer.Shutdown for its grace.
	endStreams context.CancelFunc
	listener   net.Listener
	scheduler  *cron.Scheduler
	processes  ProcessStateProvider
	webServer  *web.Server
	agentMgr   AgentManager
	// attachPlacements holds the per-attach dispatch placements registered
	// over POST /attach/placement; the web server's dispatcher reads it.
	attachPlacements *consult.AttachPlacements
	router           *sessionRouter
	claudeLookup     func() (string, error) // set via SetClaudeLookup; nil omits claude from /health
	// configSvc is the leo.yaml write path shared with the web server: both
	// serve the JSON config API from it and agree on the restart flags.
	configSvc *web.ConfigService
	logPath   string // service log path, set via SetLogPath; threaded into web.Options.LogPath by StartWeb
	// resolveHandle backs web.Options.ResolveHandle: resolves a config-defined
	// process name to its harness name and SessionHandle. Set via
	// SetResolveHandle by service boot; nil means every process is claude.
	resolveHandle func(name string) (harnessName string, h harness.SessionHandle, ok bool)

	// Observability dependencies, wired via SetObservability and threaded
	// into web.New's extra Options by StartWeb. All are optional (nil-safe on
	// the web side — see web.WithEventSource/WithActivityProvider/WithRunLog),
	// so a daemon that never calls SetObservability boots unchanged.
	observeBus        *observe.Bus
	observeRunLog     *observe.RunLog
	observeMessageLog *observe.MessageLog
	observeActivity   observe.ActivityProvider
	observeAttention  *observe.AttentionStore
	observeClock      httpapi.Clock
	leoVersion        string
	parentContext     context.Context

	// bridge is the claude mod bridge hub served on /api/bridge/*. Injected
	// with WithBridge so the service can share it with the call sites that
	// deliver through it; New builds a private one otherwise.
	bridge *bridge.Hub
	// bridgeLauncher plans bridged claude dispatch launches and
	// bridgeRouter routes agent names to their live bridges; both handed to
	// the web server by StartWeb. Either may be nil (no bridge use).
	bridgeLauncher *bridgemod.Launcher
	// leoMCP is the leo binary dispatches launch as their leo MCP server,
	// handed to the web server's consult runtime (see WithLeoMCP).
	leoMCP       leomcp.Server
	bridgeRouter *bridge.Router
	// tmuxBin is the tmux binary the daemon resolved (see WithTmuxPath);
	// empty falls back to a PATH lookup.
	tmuxBin string
	// machineStats backs GET /machine and, via StartWeb, GET /api/v1/machine;
	// nil answers 503 (see WithMachineStats).
	machineStats MachineSampler

	// control serves the agent control routes; set by StartWeb (see
	// setControl).
	control atomic.Pointer[controlHolder]
	// clientSink receives the API client policies a config reload rebuilds;
	// set by StartWeb (see setClientSink), nil until the web server exists.
	clientSink atomic.Pointer[clientSinkHolder]
	// chatHandler serves the socket's /api/v1/chat routes; set by StartWeb
	// when chat is on (see SetChat).
	chatHandler atomic.Pointer[controlHolder]
	// clientTools is the client-tools relay the socket's /api/v1/client-tools
	// routes and `?client_tools=1` provider streams serve; nil answers 503.
	clientTools        *clienttools.Relay
	chatStore          *chat.Store
	chatAttachmentsDir string
	chatPublisher      observe.Publisher
	// drain is the graceful-restart drain manager (see SetDrain) and
	// drainNotifier the web server's agent messenger it reaches agents
	// through, set by StartWeb.
	drain         atomic.Pointer[drain.Manager]
	drainNotifier atomic.Pointer[drainNotifierHolder]
	// runningDispatches lists the web server's running dispatches for the
	// drain (see RunningDispatches); set by StartWeb.
	runningDispatches atomic.Pointer[func() []drain.Probe]
	// webSources are the web server's bridge feed and dispatch counts,
	// merged into the local /state rows; set by StartWeb.
	webSources atomic.Pointer[web.AgentSources]

	// ready is reported on /health and set by MarkReady once startup has
	// restored agents; until then agent queries (e.g. /agents/stale) answer
	// from a partially populated supervisor.
	ready atomic.Bool
	// pid is this daemon's boot identity reported on /health.
	pid int
	// webListening is the web listener's bound address once it is serving,
	// reported on /health.
	webListening atomic.Pointer[healthWeb]
	// startup times StartWeb's steps; nil records nothing.
	startup *startup.Timer
	// goroutineDump is whether the process handles SIGUSR1 with a goroutine
	// dump, advertised on /health.
	goroutineDump bool
}

// Option configures a Server at construction.
type Option func(*Server)

// WithStartupTimer times StartWeb's steps through t.
func WithStartupTimer(t *startup.Timer) Option {
	return func(s *Server) { s.startup = t }
}

// WithGoroutineDump declares that the process handles SIGUSR1 with a
// goroutine dump, so /health tells `leo service dump` it is safe to signal.
// Set it only after the handler is installed.
func WithGoroutineDump() Option {
	return func(s *Server) { s.goroutineDump = true }
}

// WithBridge serves hub on the bridge routes instead of a private one.
func WithBridge(hub *bridge.Hub) Option {
	return func(s *Server) { s.bridge = hub }
}

// WithClientTools serves relay on the socket and hands the same relay to the
// web server, so apps on either listener see one set of tools.
func WithClientTools(relay *clienttools.Relay) Option {
	return func(s *Server) { s.clientTools = relay }
}

// WithLeoMCP sets the daemon's own leo binary (see leomcp.ResolveServer):
// the leo MCP server dispatches launch, and the `leo run` that cron and the
// web UI's manual runs exec.
func WithLeoMCP(s leomcp.Server) Option {
	return func(srv *Server) { srv.leoMCP = s }
}

// WithTmuxPath sets the tmux binary the daemon resolved with tmux.Resolve, so
// the IPC aborter and the web server talk to the running server's version.
func WithTmuxPath(path string) Option {
	return func(s *Server) { s.tmuxBin = path }
}

// WithBridgeLaunches hands the web server what it needs to drive claude
// agents and dispatches through the bridge: the launcher for dispatch
// launches and the router from agent names to bridge keys.
func WithBridgeLaunches(launcher *bridgemod.Launcher, router *bridge.Router) Option {
	return func(s *Server) { s.bridgeLauncher, s.bridgeRouter = launcher, router }
}

// SetObservability wires the observability event bus, run log, activity
// tracker, and build version threaded into web.Options by StartWeb. Must be
// called before StartWeb. All parameters are optional (nil/empty is safe);
// service boot is the only caller today (see internal/service/process.go).
func (s *Server) SetObservability(bus *observe.Bus, runLog *observe.RunLog, messageLog *observe.MessageLog, activity observe.ActivityProvider, version string) {
	s.observeBus = bus
	s.observeRunLog = runLog
	s.observeMessageLog = messageLog
	s.observeActivity = activity
	s.leoVersion = version
}

// SetAttention wires the per-agent attention store into the local /state
// view and, via StartWeb, the web API. Must be called before StartWeb.
func (s *Server) SetAttention(a *observe.AttentionStore) {
	s.observeAttention = a
}

// ConfigWriter returns the process-wide leo.yaml write lock shared by the
// daemon's IPC handlers and its web server.
func (s *Server) ConfigWriter() *config.Writer { return s.configWriter }

// New creates a new daemon server. The processes provider is optional (may be nil).
func New(sockPath, configPath string, processes ProcessStateProvider, opts ...Option) *Server {
	s := &Server{
		sockPath:      sockPath,
		configPath:    configPath,
		processes:     processes,
		router:        newSessionRouter(),
		parentContext: context.Background(),
		configWriter:  config.NewWriter(),
		pid:           os.Getpid(),
	}
	s.attachPlacements = consult.NewAttachPlacements(time.Now, consult.ProcessAlive)
	for _, opt := range opts {
		opt(s)
	}
	// Cron fires `leo run` from the daemon's own binary (WithLeoMCP), so an
	// isolated daemon never runs tasks through whichever leo is on PATH.
	s.scheduler = cron.New(s.leoMCP.Executable(), configPath)
	if s.bridge == nil {
		s.bridge = bridge.New(bridge.Options{})
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
		return tmux.AbortPrompt(context.Background(), s.tmuxPath(), tmuxSession)
	})

	s.configSvc = s.newConfigService()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /version", s.handleVersion)
	mux.HandleFunc("GET /events", s.handleEvents)
	mux.HandleFunc("GET /state", s.handleState)
	mux.HandleFunc("GET /machine", s.handleMachine)
	mux.HandleFunc("GET /templates", s.handleTemplates)
	mux.HandleFunc("GET /environments", s.handleEnvironments)
	mux.HandleFunc("POST /cron/install", s.handleCronInstall)
	mux.HandleFunc("POST /cron/remove", s.handleCronRemove)
	mux.HandleFunc("GET /cron/list", s.handleCronList)
	mux.HandleFunc("POST /task/add", s.handleTaskAdd)
	mux.HandleFunc("POST /task/remove", s.handleTaskRemove)
	mux.HandleFunc("POST /task/enable", s.handleTaskEnable)
	mux.HandleFunc("POST /task/disable", s.handleTaskDisable)
	mux.HandleFunc("GET /task/list", s.handleTaskList)
	// The JSON config API (docs/configuration/config-api.md): the 0600 socket
	// is the operator's, so no wrapper. /config/reload is the shared service's
	// too, so socket and web answer with the same body.
	s.configSvc.RegisterConfigRoutes(mux, "", func(h http.HandlerFunc) http.HandlerFunc { return h })
	s.configSvc.RegisterReloadRoute(mux, "", func(h http.HandlerFunc) http.HandlerFunc { return h })
	mux.HandleFunc("GET /drain/busy", s.handleDrainBusy)
	mux.HandleFunc("POST /drain", s.handleDrainBegin)
	mux.HandleFunc("GET /drain", s.handleDrainStatus)
	mux.HandleFunc("POST /drain/commit", s.handleDrainCommit)
	mux.HandleFunc("POST /drain/cancel", s.handleDrainCancel)

	// Task-run observability: a `leo run` subprocess (cron, web run-now, or
	// manual invocation) has no in-process access to the daemon's RunLog, so
	// it reports task_run_* events here instead. See wireObservability and
	// internal/run.SetPublisher's doc comments for why the daemon-side
	// producer path (Supervisor) differs from this subprocess-side one.
	mux.HandleFunc("POST /observe/task-run", s.handleObserveTaskRun)

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
	mux.HandleFunc("GET /agents/stale", s.handleAgentStale)
	mux.HandleFunc("POST /agents/{name}/stop", s.handleAgentStop)
	mux.HandleFunc("POST /agents/{name}/start", s.handleAgentStart)
	mux.HandleFunc("POST /agents/{name}/reset", s.handleAgentReset)
	mux.HandleFunc("POST /agents/{name}/restart", s.handleAgentRestart)
	mux.HandleFunc("POST /agents/{name}/set-template", s.handleAgentSetTemplate)
	mux.HandleFunc("POST /agents/{name}/set-environment", s.handleAgentSetEnvironment)
	// Socket twin of POST /api/v1/agents/{name}/environments (Leo Term's path).
	mux.HandleFunc("POST /agents/{name}/environments", s.handleAgentSetEnvironment)
	// The same handler serves /api/v1, so both answer with the same bytes.
	mux.HandleFunc("PUT /agents/{name}/appearance", web.AgentAppearanceHandler(s.appearanceService))
	mux.HandleFunc("DELETE /agents/{name}", s.handleAgentDelete)
	mux.HandleFunc("GET /agents/{name}/delete-plan", s.handleAgentDeletePlan)
	mux.HandleFunc("POST /agents/{name}/rename", s.handleAgentRename)
	mux.HandleFunc("GET /agents/{name}/logs", s.handleAgentLogs)
	mux.HandleFunc("GET /agents/{name}/session", s.handleAgentSession)
	mux.HandleFunc("GET /agents/{name}/attach-spec", s.handleAgentAttachSpec)
	mux.HandleFunc("POST /attach/placement", s.handleAttachPlacement)
	for _, verb := range controlVerbs {
		mux.HandleFunc("POST /agents/{name}/"+verb, s.handleAgentControl)
	}

	// Claude mod bridge: the mod's `leo bridge` process holds the command
	// stream open and posts acks/events back. The 0600 socket is the auth,
	// as for every route on this mux.
	// Client-tools relay: the same paths as the TCP API, for the Mac app.
	clientTools := web.NewClientToolsHandler(func() *clienttools.Relay { return s.clientTools })
	mux.Handle(clientToolsSocketPath, clientTools)
	mux.Handle(clientToolsSocketPath+"/", clientTools)
	mux.HandleFunc(chatSocketPrefix, s.handleChat)
	mux.HandleFunc("POST "+chatTaskFailurePath, s.handleChat)
	mux.HandleFunc("GET /api/bridge/{agent}/stream", s.handleBridgeStream)
	mux.HandleFunc("POST /api/bridge/{agent}/report", s.handleBridgeReport)

	baseCtx, endStreams := context.WithCancel(context.Background())
	s.endStreams = endStreams
	s.httpServer = &http.Server{
		Handler:      mux,
		BaseContext:  func(net.Listener) context.Context { return baseCtx },
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	return s
}

// newConfigService builds the config service the socket and the web server
// share. Its collaborators are late-bound closures: the agent manager, log
// path and version are attached after New returns.
func (s *Server) newConfigService() *web.ConfigService {
	return web.NewConfigService(web.ConfigServiceOptions{
		ConfigPath: s.configPath,
		Writer:     s.configWriter,
		Reloader:   s,
		LeoPath:    s.leoMCP.Executable(),
		LogPath:    func() string { return s.logPath },
		Agents: func() web.AgentRestarter {
			if s.agentMgr == nil {
				return nil
			}
			return s.agentMgr
		},
		Processes: func() web.ProcessStateProvider { return &processAdapter{inner: s.processes} },
		Version:   func() string { return s.leoVersion },
		StartedAt: time.Now(),
		PID:       s.pid,
	})
}

// SetParentContext supplies the service lifetime to web-dispatched runs.
// It must be called before StartWeb.
func (s *Server) SetParentContext(ctx context.Context) {
	if ctx != nil {
		s.parentContext = ctx
	}
}

// Handler exposes the Unix-socket mux for in-process clients and tests.
func (s *Server) Handler() http.Handler { return s.httpServer.Handler }

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

	clients := clientPolicies(cfg)

	port := cfg.WebPort()
	var observeOpts []web.Option
	if s.observeBus != nil {
		observeOpts = append(observeOpts, web.WithEventSource(s.observeBus))
	}
	if s.observeRunLog != nil {
		observeOpts = append(observeOpts, web.WithRunLog(s.observeRunLog))
	}
	if s.observeMessageLog != nil {
		// Read seam for recent_messages, plus the write seam the web layer
		// publishes agent-to-agent activity through. The message log wraps
		// the rest of the chain, so publishing through it records first and
		// then forwards to the bus.
		observeOpts = append(observeOpts,
			web.WithMessageLog(s.observeMessageLog),
			web.WithPublisher(s.observeMessageLog))
	}
	if s.observeActivity != nil {
		observeOpts = append(observeOpts, web.WithActivityProvider(s.observeActivity))
	}
	if s.observeAttention != nil {
		observeOpts = append(observeOpts, web.WithAttention(s.observeAttention))
	}
	if s.leoVersion != "" {
		observeOpts = append(observeOpts, web.WithVersion(s.leoVersion))
	}
	if s.machineStats != nil {
		observeOpts = append(observeOpts, web.WithMachineStats(s.machineStats))
	}
	// Supervised agents' turn hooks complete their in-flight persistent-task
	// invocations, whatever the harness (see sessionRouter.ObserveTurn).
	observeOpts = append(observeOpts, web.WithTurnObserver(s.observeAgentTurn))
	if s.clientTools != nil {
		observeOpts = append(observeOpts, web.WithClientTools(s.clientTools))
	}
	stop := s.startup.Step("web.New")
	s.webServer = web.New(s.configPath, &processAdapter{inner: s.processes}, s.scheduler, s, agentSvc, web.Options{
		Config:         s.configSvc,
		ConfigWriter:   s.configWriter,
		Port:           port,
		APIToken:       apiToken,
		AgentToken:     agentToken,
		Clients:        clients,
		AllowedHosts:   cfg.Web.AllowedHosts,
		TrustedProxies: cfg.Web.TrustedProxies,
		LogPath:        s.logPath,
		Bridge:         web.BridgeOptions{Router: s.bridgeRouter, Launcher: s.bridgeLauncher},
		ResolveHandle:  s.resolveHandle,
		// Consults record to <state>/consults for `leo consult watch`.
		ConsultRecorder:  consult.NewFileRecorder(cfg.StatePath()),
		ParentContext:    s.parentContext,
		LeoMCP:           s.leoMCP,
		TmuxPath:         s.tmuxBin,
		AttachPlacements: s.attachPlacements,
		// Paired phones (leo pair): device tokens and POST /api/v1/pair.
		Devices:    devices.NewStore(cfg.StatePath(), nil),
		PublicURLs: cfg.Web.PublicURLs,
		Chat:       web.ChatOptions{Store: s.chatStore, AttachmentsDir: s.chatAttachmentsDir, Publisher: s.chatPublisher, TaskRun: s.TaskRun},
		Startup:    s.startup,
	}, observeOpts...)
	stop()
	s.setControl(s.webServer.ControlHandler())
	s.setClientSink(s.webServer)
	s.setDrainNotifier(s.webServer.DrainNotifier())
	s.setRunningDispatches(s.webServer.RunningDispatches)
	if s.chatStore != nil {
		stop = s.startup.Step("StartChat/Recover")
		s.webServer.StartChat(s.parentContext)
		stop()
		s.setChat(s.webServer.ChatHandler())
	}
	s.setWebSources(s.webServer.ObserveSources())
	bind := cfg.WebBind()
	addr := fmt.Sprintf("%s:%d", bind, port)
	stop = s.startup.Step("listener bind")
	err = s.webServer.ListenAndServe(addr)
	stop()
	if err != nil {
		return fmt.Errorf("starting web UI: %w", err)
	}
	s.setWebListening(bind, port)
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

// clientSetter is the part of the web server a reload swaps API client
// policies into.
type clientSetter interface {
	SetClients([]web.ClientPolicy)
}

// clientSinkHolder lets an interface value sit in an atomic.Pointer.
type clientSinkHolder struct{ c clientSetter }

// setClientSink installs the web server a reload swaps client policies into.
// StartWeb sets it after the socket is already serving, hence the atomic.
func (s *Server) setClientSink(c clientSetter) {
	s.clientSink.Store(&clientSinkHolder{c: c})
}

// clientPolicies builds the web policies for cfg's api_clients, each with its
// scoped token. A client whose token cannot be prepared is dropped with a
// warning rather than failing the caller: the fleet must keep running when one
// container's credential is unreadable.
func clientPolicies(cfg *config.Config) []web.ClientPolicy {
	var clients []web.ClientPolicy
	for name, client := range cfg.APIClients {
		token, err := web.EnsureClientToken(cfg.StatePath(), name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: api client %q disabled: %v\n", name, err)
			continue
		}
		clients = append(clients, web.ClientPolicy{
			Name:       name,
			Token:      token,
			CanMessage: client.CanMessage,
			CanPost:    client.CanPost,
		})
	}
	return clients
}

// ReloadConfig reloads config from disk, swaps the web server's API client
// policies, republishes agent appearance, and re-syncs the scheduler.
// Implements web.ConfigReloader.
//
// The client swap comes first, so an unrelated reload failure below cannot
// leave a removed client with access. A config that will not load leaves the
// running policies untouched.
func (s *Server) ReloadConfig() error {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if sink := s.clientSink.Load(); sink != nil {
		sink.c.SetClients(clientPolicies(cfg))
	}
	// Appearance resolves from config and the agent records alone, so it is
	// republished even when a later step of the reload fails.
	if s.agentMgr != nil {
		s.agentMgr.RepublishAppearance()
	}
	if cfg.UsesHarness("opencode") {
		if err := leomcp.EnsureOpenCodeContext(cfg); err != nil {
			return fmt.Errorf("refresh opencode context: %w", err)
		}
	}
	return s.scheduler.Install(cfg)
}

// AgentGone tells the daemon an agent is gone (stopped or deleted): its open
// chat asks close and its parked client-tool calls are cancelled.
func (s *Server) AgentGone(name string) {
	s.CloseChatAsksOf(name)
	if s.clientTools != nil {
		s.clientTools.CancelAgent(name)
	}
}

// CloseChatAsksOf closes a gone (stopped or deleted) agent's open chat asks.
// A no-op until StartWeb has run, and with chat off.
func (s *Server) CloseChatAsksOf(name string) {
	if s.webServer != nil {
		s.webServer.CloseChatAsksOf(name)
	}
}

// Shutdown gracefully stops the server and removes the socket file.
func (s *Server) Shutdown() error {
	s.endStreams()
	s.scheduler.Stop()

	if s.webServer != nil {
		s.webServer.Shutdown() //nolint:errcheck
	}

	// Stop the router so its pump and janitor goroutines exit.
	if s.router != nil {
		s.router.Stop()
	}

	// End bridge streams first: an open stream is an active request, which
	// httpServer.Shutdown would otherwise wait out for its full grace.
	s.bridge.Close()

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

// MarkReady records that startup has finished restoring agents, so /health
// reports ready. The boot path must call it once restore returns — whether
// or not anything was restored — or WaitReady callers time out.
func (s *Server) MarkReady() {
	s.ready.Store(true)
}

// StartRestartReconcile starts the web server's background restart
// reconcile, which finishes reconciling the dispatches the previous daemon
// left. The boot path calls it right after MarkReady, so that work never
// delays readiness. A no-op without a web server.
func (s *Server) StartRestartReconcile() {
	if s.webServer != nil {
		s.webServer.StartRestartReconcile()
	}
}

// Ready reports whether MarkReady has been called. The agent Manager reads it
// to tell an agent awaiting restore from a stopped one.
func (s *Server) Ready() bool {
	return s.ready.Load()
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeData(w, http.StatusOK, healthData{Version: s.leoVersion, Ready: s.ready.Load(), PID: s.pid, Web: s.webListening.Load(), Claude: s.claudeHealth(), GoroutineDump: s.goroutineDump})
}

// SetClaudeLookup wires the claude resolution /health reports. It is called
// per request, so a CLI installed after the daemon started shows up at once.
func (s *Server) SetClaudeLookup(lookup func() (string, error)) {
	s.claudeLookup = lookup
}

func (s *Server) claudeHealth() *healthClaude {
	if s.claudeLookup == nil {
		return nil
	}
	path, err := s.claudeLookup()
	if err != nil {
		return &healthClaude{}
	}
	return &healthClaude{Found: true, Path: path}
}

// setWebListening records the address the web listener is serving on.
func (s *Server) setWebListening(bind string, port int) {
	s.webListening.Store(&healthWeb{Bind: bind, Port: port})
}

type taskEnqueueReq struct {
	InvocationID   string   `json:"invocation_id,omitempty"`
	Session        string   `json:"session"`
	TmuxSession    string   `json:"tmux_session,omitempty"`
	Task           string   `json:"task"`
	RunID          string   `json:"run_id,omitempty"`
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
		RunID:       req.RunID,
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

// observeAgentTurn relays one supervised agent's turn hook to the router,
// whose queues are keyed by agent name.
func (s *Server) observeAgentTurn(agent string, turn web.AgentTurn) {
	s.router.ObserveTurn(agent, TurnEvent{
		Event:       turn.Event,
		Prompt:      turn.Prompt,
		TurnID:      turn.TurnID,
		SessionID:   turn.SessionID,
		LastMessage: turn.LastMessage,
		Error:       turn.Error,
	})
}

// TaskRun returns the persistent task and run whose turn is running in
// agent, if any: what its chat tools file their output under.
func (s *Server) TaskRun(agent string) (task, runID string, ok bool) {
	inv, ok := s.router.InFlight(agent)
	if !ok {
		return "", "", false
	}
	return inv.Task, inv.RunID, true
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

// observeTaskRunReq is the wire body for POST /observe/task-run. Type must be
// one of the observe.EventTaskRun* constants; anything else is rejected
// rather than forwarded, since this route's only purpose is relaying the
// three task-run event types a subprocess producer can emit.
type observeTaskRunReq struct {
	Type string          `json:"type"`
	Run  observe.TaskRun `json:"run"`
}

// observeTaskRunStatusForType maps each valid event Type to the Status the
// server assigns the run — the two can never disagree, because the client's
// own Run.Status is ignored entirely and overwritten with this value. This
// closes the gap where a caller could post task_run_started with no (or a
// bogus) status and have it land verbatim in the RunLog, breaking the wire
// contract's guarantee that Status is always one of exactly three values.
var observeTaskRunStatusForType = map[string]observe.RunStatus{
	string(observe.EventTaskRunStarted):   observe.RunRunning,
	string(observe.EventTaskRunSucceeded): observe.RunSucceeded,
	string(observe.EventTaskRunFailed):    observe.RunFailed,
}

// maxObserveTaskRunBodyBytes bounds the request body for POST
// /observe/task-run. Unlike the web (TCP) listener, the socket mux this route
// lives on has no body-size middleware, and this route's input is unusually
// dangerous to leave unbounded: it is retained (up to observe.MaxRecentRuns
// entries in the RunLog) and rebroadcast to every SSE subscriber, rather than
// discarded after one use like most handlers' inputs. Generous relative to
// the per-field caps below (which do the real work of bounding what's kept)
// so a legitimate request is never at risk of tripping this first.
const maxObserveTaskRunBodyBytes int64 = 64 << 10 // 64 KiB

// handleObserveTaskRun relays a task-run event from a `leo run` subprocess
// into the daemon's RunLog, so it is recorded and fanned out to /api/v1/state
// and /api/v1/events exactly as if it had been published in-process. A nil
// RunLog (daemon started without SetObservability) makes this a safe no-op —
// the caller (PublishTaskRun) treats any response as best-effort anyway.
func (s *Server) handleObserveTaskRun(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxObserveTaskRunBodyBytes)

	var req observeTaskRunReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	status, ok := observeTaskRunStatusForType[req.Type]
	if !ok {
		writeError(w, http.StatusBadRequest, "unsupported event type: "+req.Type)
		return
	}
	if req.Run.ID == "" || req.Run.Task == "" {
		// RunLog.record matches by ID, so an empty ID would collapse every
		// ID-less run into one slot, silently overwriting one another; an
		// empty Task breaks any consumer that displays or groups by it.
		writeError(w, http.StatusBadRequest, "run.id and run.task are required")
		return
	}

	req.Run.Status = status
	req.Run = observe.ClampRunFields(req.Run)

	if s.observeRunLog != nil {
		s.observeRunLog.Publish(observe.Event{
			Type:    observe.EventType(req.Type),
			Payload: &observe.TaskRunPayload{Run: req.Run},
		})
	}
	writeJSON(w, http.StatusOK, Response{OK: true})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, Response{OK: false, Error: msg})
}

// tmuxPath is the daemon-resolved tmux binary, else a PATH lookup falling
// back to "tmux". The lookup runs on each call so PATH changes in tests are
// observable.
func (s *Server) tmuxPath() string {
	if s.tmuxBin != "" {
		return s.tmuxBin
	}
	p, err := tmux.Locate()
	if err != nil {
		return "tmux"
	}
	return p
}

// AttachPlacements is the registry of per-attach dispatch placements.
func (s *Server) AttachPlacements() *consult.AttachPlacements { return s.attachPlacements }
