package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/harness"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/history"
	"github.com/blackpaw-studio/leo/internal/observe"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// ProcessStateInfo mirrors daemon.ProcessStateInfo to avoid import cycle.
type ProcessStateInfo struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at"`
	Restarts  int       `json:"restarts"`
	Ephemeral bool      `json:"ephemeral,omitempty"`
}

// ProcessStateProvider returns the state of all supervised processes.
type ProcessStateProvider interface {
	States() map[string]ProcessStateInfo
}

// SchedulerProvider exposes cron entry listing.
type SchedulerProvider interface {
	List() []cron.EntryInfo
}

// ConfigReloader reloads config and re-syncs the scheduler.
type ConfigReloader interface {
	ReloadConfig() error
}

// AgentService owns the ephemeral-agent lifecycle. It is implemented by
// *agent.Manager; web handlers delegate to it instead of driving the supervisor
// directly, so the same code path backs the web UI, channel plugins, the
// daemon socket, and the CLI. A nil AgentService disables agent UI features.
type AgentService interface {
	Spawn(ctx context.Context, spec agent.SpawnSpec) (agent.Record, error)
	Stop(name string) error
	List() []agent.Record
	Resolve(query string) (agent.Record, error)
	Rename(query, newName string) (agent.Record, error)
	Suspend(name string) error
	Resume(name string) (agent.Record, error)
	// RestartAll bounces every live agent in place (skipping suspended/
	// stopped ones), re-applying current config for template-spawned agents.
	// Backs POST /web/agents/restart.
	RestartAll() agent.RestartResult
	// ResolveHandle resolves an agent name to its harness name and the
	// SessionHandle a SessionDriver needs to deliver a message to it.
	// ok=false means "not an ephemeral agent" (unknown name, or no
	// agentstore record) — callers fall back to the tmux-based claude path.
	ResolveHandle(name string) (harnessName string, h harness.SessionHandle, ok bool)
}

// Server serves the Leo web UI over HTTP.
type Server struct {
	configPath string
	processes  ProcessStateProvider
	scheduler  SchedulerProvider
	reloader   ConfigReloader
	agentSvc   AgentService
	leoPath    string
	templates  *template.Template
	httpServer *http.Server
	listener   net.Listener
	// serviceRestartNeeded is set when a Web UI config save (port/bind/
	// enabled) changes settings that only take effect when the daemon's TCP
	// listener is rebuilt at boot. Cleared by handleServiceRestart.
	serviceRestartNeeded atomic.Bool
	// agentsRestartNeeded is set when a Defaults or Template config save
	// changes settings a *running* agent won't see until it's individually
	// restarted (config reload already makes new spawns/task runs pick it up
	// immediately — see applySection). Cleared by handleAgentsRestart on a
	// restart batch with no failures.
	agentsRestartNeeded atomic.Bool
	port                int           // port the listener is expected to bind on; used for Host/Origin checks
	apiToken            string        // operator bearer token: /api/*, browser routes, and /login
	agentToken          string        // token exported to agents; /api/* and agent-messaging routes only
	allowedHosts        []string      // extra hosts permitted beyond loopback (e.g. LAN IPs)
	sessions            *sessionStore // in-memory browser sessions for cookie-based auth

	// serviceLogPath is the absolute path to the service log tailed by the
	// Service page's log viewer. Computed by service.LogPathFor(homePath) —
	// the only place that formula lives — and threaded in via Options.LogPath
	// since internal/web cannot import internal/service (import cycle:
	// service -> daemon -> web). Empty when not wired by the caller (e.g. in
	// tests that don't need it), in which case the log tail handler renders
	// a "not configured" message instead of guessing a path.
	serviceLogPath string

	// agentMu guards the on-demand, 60s-TTL cache of claude sub-agent names
	// used to populate dropdowns without shelling out on every render.
	agentMu       sync.Mutex
	agentCache    []string
	agentsFetched time.Time

	// fetchAgentListFn is invoked outside agentMu to refresh agentCache.
	// Defaults to s.fetchAgentList; tests replace it to control timing
	// without shelling out to a real `claude` binary.
	fetchAgentListFn func() []string

	// Testability seam for exec.Command
	execCommand func(name string, args ...string) *exec.Cmd

	// afterInterruptBurst, if non-nil, is invoked after
	// handleWebAgentInterrupt's background delayed-Escape goroutine finishes
	// all its attempts. nil in production (no-op); tests replace it with a
	// channel send to deterministically wait for the goroutine instead of
	// sleeping past its bounded (~interruptDelayedAttempts*interruptDelayedPoll)
	// duration, so no goroutine survives past its own test and races the
	// next test's use of the package-level interrupt-burst timing vars.
	afterInterruptBurst func()

	// injectPrompt delivers a message into a tmux session via the readiness-
	// probing path (tmux.InjectPrompt). Tests replace this to verify the
	// resumed-agent message delivery path without requiring a real tmux session.
	injectPrompt func(ctx context.Context, session, body string) error

	// resolveHandle resolves a config-defined *process* name to its harness
	// name and SessionHandle, mirroring AgentService.ResolveHandle for
	// agents. Wired at service boot (the layer that owns []ProcessSpec and
	// live procIdentity argv), the same way injectPrompt's real
	// implementation is normally supplied by the caller that owns tmux.
	// nil or a false ok return means "not found" or "claude" — the caller
	// falls back to today's tmux path either way.
	resolveHandle func(name string) (harnessName string, h harness.SessionHandle, ok bool)

	// consults runs synchronous one-off consultant subagents (leo_consult).
	consults *consult.Dispatcher

	// activity is the read seam onto the activity tracker (internal/observe),
	// wired via WithActivityProvider. nil is a supported default: every agent
	// reports observe.ActivityUnknown in that case, so the observability API
	// works before the tracker exists / when it's not configured.
	activity observe.ActivityProvider

	// events is the read seam onto the (not-yet-existing) event bus, wired via
	// WithEventSource. nil is a supported default: GET /api/v1/events still
	// serves hello + heartbeats, just no bus-published events.
	events eventSource

	// runLog is the read seam onto the run log (internal/observe.RunLog),
	// wired via WithRunLog. nil is a supported default: recent_runs is then
	// built from task history alone, so in-flight runs simply don't appear
	// (matching behavior before the run log existed) rather than erroring.
	runLog runProvider

	// publisher is the write seam onto the event bus, wired via
	// WithPublisher. Only agent-to-agent message routing publishes from the
	// web layer; everything else publishes from the supervisor. nil is a
	// supported default, making publishAgentMessage a no-op.
	publisher observe.Publisher

	// messageLog is the read seam onto the agent-message log
	// (internal/observe.MessageLog), wired via WithMessageLog. nil is a
	// supported default: recent_messages is then empty, as with runLog.
	messageLog messageProvider

	// version is reported as Snapshot.LeoVersion. Wired via WithVersion;
	// empty means the caller didn't provide one.
	version string

	// sseHeartbeat is the interval between SSE comment heartbeats on
	// GET /api/v1/events. Defaults to defaultSSEHeartbeat; tests shrink it
	// directly (same package) to avoid a 20s wait.
	sseHeartbeat time.Duration

	// sseWriteTimeout bounds each individual write on GET /api/v1/events
	// (hello, heartbeat, and event frames). Defaults to
	// defaultSSEWriteTimeout; tests shrink it directly (same package).
	sseWriteTimeout time.Duration
}

// eventSource is the narrow subscribe seam onto the event bus. Defined here
// rather than depending on the bus's concrete type, which doesn't exist in
// this package's dependency graph (internal/observe only defines the
// Publisher side) — this keeps /api/v1/events buildable and testable ahead of
// the bus landing. buffer is the subscriber's bounded channel size; a slow
// consumer that fills it is expected to be dropped by the implementation
// (the returned channel closes), not blocked on indefinitely.
type eventSource interface {
	// Subscribe registers a new subscriber and returns its channel, an
	// unsubscribe function, and the sequence number of the last event
	// published before this subscriber was registered (0 if none yet) — all
	// atomically, so the stream's opening hello frame can report a real
	// starting point with the guarantee that the first event this
	// subscriber actually receives is exactly that seq+1. See
	// observe.Bus.Subscribe's doc comment.
	Subscribe(buffer int) (<-chan observe.Event, func(), uint64)
}

// runProvider is the narrow read seam onto the run log (internal/observe's
// RunLog satisfies it structurally). Defined here rather than depending on
// RunLog's concrete type, mirroring eventSource above.
type runProvider interface {
	Recent(n int) []observe.TaskRun
}

// messageProvider is the narrow read seam onto the agent-message log
// (internal/observe's MessageLog satisfies it structurally), mirroring
// runProvider above. now is passed in rather than read inside so the age
// window stays testable without a clock seam.
type messageProvider interface {
	Recent(n int, now time.Time) []observe.AgentMessage
}

// Option configures optional Server dependencies that are not required for
// the server's existing functionality to keep working unchanged. Passing no
// options preserves pre-existing behavior for every current caller.
type Option func(*Server)

// WithActivityProvider wires the activity tracker's read seam so
// GET /api/v1/state and GET /api/v1/events can report live agent activity.
// Optional; omitting it makes every agent report observe.ActivityUnknown.
func WithActivityProvider(p observe.ActivityProvider) Option {
	return func(s *Server) { s.activity = p }
}

// WithEventSource wires the event bus that GET /api/v1/events streams from.
// Optional; omitting it makes the stream serve only a hello event plus
// heartbeats (no bus-published events).
func WithEventSource(es eventSource) Option {
	return func(s *Server) { s.events = es }
}

// WithRunLog wires the run log GET /api/v1/state reads recent_runs from.
// Optional; omitting it makes recent_runs built from task history alone
// (no in-flight runs, and less honest completed-run timing on entries
// recorded before the run log existed).
func WithRunLog(rl runProvider) Option {
	return func(s *Server) { s.runLog = rl }
}

// WithMessageLog wires the agent-message log GET /api/v1/state reads
// recent_messages from. Optional; omitting it makes recent_messages empty.
func WithMessageLog(ml messageProvider) Option {
	return func(s *Server) { s.messageLog = ml }
}

// WithPublisher wires the event bus the web layer publishes agent-to-agent
// message activity to. Optional; omitting it silently skips those events,
// leaving every other observability signal unaffected.
func WithPublisher(p observe.Publisher) Option {
	return func(s *Server) { s.publisher = p }
}

// WithVersion sets the Leo build version reported as Snapshot.LeoVersion by
// GET /api/v1/state. Optional; omitting it reports an empty string.
func WithVersion(v string) Option {
	return func(s *Server) { s.version = v }
}

// Options bundles the knobs the web server needs that aren't part of the
// provider interfaces. Zero values disable the corresponding surface:
//   - Port must match the listener port so Host/Origin checks pass.
//   - APIToken must be non-empty for /api/* routes to work. If empty, /api/*
//     responds 500 to avoid accidentally serving the API unauthenticated.
type Options struct {
	Port     int
	APIToken string
	// AgentToken is the less-privileged token handed to spawned agents as
	// LEO_API_TOKEN. Accepted on /api/* and the agent-messaging routes, and
	// rejected at /login and on the rest of the browser UI. Empty means only
	// APIToken is accepted anywhere (pre-split behaviour).
	AgentToken   string
	AllowedHosts []string
	// LogPath is the absolute path to the service log, computed by
	// service.LogPathFor(homePath) at the layer that can import
	// internal/service (see internal/daemon/server.go's StartWeb). Optional;
	// empty disables the Service page's log tail (renders a "not
	// configured" message instead of guessing a path).
	LogPath string
	// ResolveHandle resolves a config-defined process name to its harness
	// name and SessionHandle. Optional; nil means every process is treated
	// as claude (today's behavior). Wired from service boot the same way
	// LogPath is — see internal/service/process.go.
	ResolveHandle func(name string) (harnessName string, h harness.SessionHandle, ok bool)
	// ConsultRecorder persists consult records and event streams so
	// `leo consult list` / `leo consult watch` can see work in flight.
	// Optional; nil discards recordings.
	ConsultRecorder consult.Recorder
}

// New creates a new web UI server. agentSvc may be nil if agent spawning is
// not available. extra applies optional dependencies (activity provider,
// event bus, version) via the functional-option pattern — every existing
// caller passing none keeps its current behavior unchanged.
func New(configPath string, processes ProcessStateProvider, scheduler SchedulerProvider, reloader ConfigReloader, agentSvc AgentService, opts Options, extra ...Option) *Server {
	leoPath, err := exec.LookPath("leo")
	if err != nil {
		leoPath = "leo"
	}

	s := &Server{
		configPath:      configPath,
		processes:       processes,
		scheduler:       scheduler,
		reloader:        reloader,
		agentSvc:        agentSvc,
		leoPath:         leoPath,
		port:            opts.Port,
		apiToken:        opts.APIToken,
		agentToken:      opts.AgentToken,
		allowedHosts:    opts.AllowedHosts,
		serviceLogPath:  opts.LogPath,
		execCommand:     exec.Command,
		resolveHandle:   opts.ResolveHandle,
		sseHeartbeat:    defaultSSEHeartbeat,
		sseWriteTimeout: defaultSSEWriteTimeout,
	}
	for _, opt := range extra {
		opt(s)
	}
	s.fetchAgentListFn = s.fetchAgentList
	s.consults = consult.NewDispatcher(opts.ConsultRecorder)

	s.injectPrompt = func(ctx context.Context, session, body string) error {
		return tmux.InjectPrompt(ctx, findTmuxPath(), session, body)
	}

	s.sessions = newSessionStore(sessionTTL)
	s.parseTemplates()

	mux := http.NewServeMux()

	// Static assets
	staticFS, _ := fs.Sub(content, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// Login / logout (unprotected by sessionMiddleware; they are what grants the session).
	mux.HandleFunc("GET /login", s.loginHandler)
	mux.HandleFunc("POST /login", s.loginHandler)
	mux.HandleFunc("POST /logout", s.logoutHandler)

	// Full page — / redirects to the default section; every other section
	// has its own route rendered through handlePage (handlers_pages.go).
	//
	// {$} keeps this exact: a bare "GET /" is a prefix pattern matching every
	// path, which makes unknown routes report a misleading 405 instead of 404.
	// See the unknown-route tests in web_test.go.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/tasks", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /tasks", s.handlePage("tasks", "Tasks", s.buildTasksData))
	mux.HandleFunc("GET /tasks/{name}", s.handleTaskEditPage)
	mux.HandleFunc("GET /agents", s.handlePage("agents", "Agents", s.buildAgentsData))
	mux.HandleFunc("GET /config/defaults", s.handlePage("config_defaults", "Defaults", s.buildDefaultsData))
	mux.HandleFunc("GET /config/templates", s.handlePage("config_templates", "Templates", s.buildTemplatesData))
	mux.HandleFunc("GET /config/templates/{name}", s.handleTemplateEditPage)
	mux.HandleFunc("GET /config/settings", s.handlePage("config_settings", "Settings", s.buildSettingsData))
	mux.HandleFunc("GET /service", s.handlePage("service", "Service", s.buildServiceData))

	// Partials (htmx polling targets)
	mux.HandleFunc("GET /partials/status", s.handlePartialStatus)
	mux.HandleFunc("GET /partials/task/{name}/history", s.handlePartialTaskHistory)
	mux.HandleFunc("GET /partials/task/{name}/log", s.handleTaskRunLog)

	// Utilities
	mux.HandleFunc("GET /web/cron/preview", s.handleCronPreview)
	mux.HandleFunc("GET /web/partials/harness-options", s.handleHarnessOptionsPartial)

	// Task mutations
	mux.HandleFunc("POST /web/task/{name}/toggle", s.handleTaskToggle)
	mux.HandleFunc("POST /web/task/{name}/run", s.handleTaskRun)

	// Config mutations
	mux.HandleFunc("POST /web/config/reload", s.handleConfigReload)
	mux.HandleFunc("POST /web/config/defaults", s.handleConfigDefaultsSave)
	mux.HandleFunc("POST /web/config/task/{name}", s.handleConfigTaskSave)

	// Task CRUD
	mux.HandleFunc("POST /web/task/add", s.handleTaskAdd)
	mux.HandleFunc("DELETE /web/task/{name}/delete", s.handleTaskDelete)

	// Prompt file editing
	mux.HandleFunc("GET /web/task/{name}/prompt", s.handleTaskPromptGet)
	mux.HandleFunc("POST /web/task/{name}/prompt", s.handleTaskPromptSave)

	// Template config management
	mux.HandleFunc("POST /web/config/template/{name}", s.handleConfigTemplateSave)
	mux.HandleFunc("POST /web/template/add", s.handleTemplateAdd)
	mux.HandleFunc("DELETE /web/template/{name}", s.handleTemplateDelete)
	mux.HandleFunc("POST /web/template/{name}/rename", s.handleTemplateRename)

	// Settings page: Web UI + Remote client config, and remote-host CRUD —
	// full CRUD lives on one page (no separate edit page).
	mux.HandleFunc("POST /web/config/web", s.handleConfigWebSave)
	mux.HandleFunc("POST /web/config/client", s.handleConfigClientSave)
	mux.HandleFunc("POST /web/config/host/{name}", s.handleConfigHostSave)
	mux.HandleFunc("POST /web/host/add", s.handleHostAdd)
	mux.HandleFunc("DELETE /web/host/{name}", s.handleHostDelete)

	// Service control
	mux.HandleFunc("POST /web/service/restart", s.handleServiceRestart)
	mux.HandleFunc("GET /web/service/logtail", s.handleServiceLogTail)

	// Agent fleet control: bounce every running agent in place, re-applying
	// current config for template-spawned agents (see agent.Manager.Restart).
	mux.HandleFunc("POST /web/agents/restart", s.handleAgentsRestart)

	// Agent management (web UI). handlePartialAgents (agents.html re-render
	// after a rename) is invoked directly by handleWebAgentRename, not
	// routed — see handlers_agents.go.
	mux.HandleFunc("POST /web/agent/spawn", s.handleWebAgentSpawn)
	mux.HandleFunc("POST /web/agent/{name}/stop", s.handleWebAgentStop)
	mux.HandleFunc("POST /web/agent/{name}/suspend", s.handleWebAgentSuspend)
	mux.HandleFunc("POST /web/agent/{name}/resume", s.handleWebAgentResume)
	mux.HandleFunc("POST /web/agent/{name}/rename", s.handleWebAgentRename)
	mux.HandleFunc("POST /web/agent/{name}/send", s.handleWebAgentSendKeys)
	mux.HandleFunc("POST /web/agent/{name}/interrupt", s.handleWebAgentInterrupt)
	mux.HandleFunc("POST /web/agent/{name}/message", s.handleWebAgentMessage)

	// Agent + task management (JSON API — used by channel plugins and external
	// clients). Registered on a sub-mux so we can wrap /api/* in bearer auth
	// without affecting the browser-facing /web/* routes.
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("POST /api/agent/spawn", s.handleAPIAgentSpawn)
	apiMux.HandleFunc("POST /api/agent/stop", s.handleAPIAgentStop)
	apiMux.HandleFunc("POST /api/agent/suspend", s.handleAPIAgentSuspend)
	apiMux.HandleFunc("POST /api/agent/resume", s.handleAPIAgentResume)
	apiMux.HandleFunc("POST /api/agent/{name}/rename", s.handleAPIAgentRename)
	apiMux.HandleFunc("POST /api/consult", s.handleAPIConsult)
	apiMux.HandleFunc("GET /api/agent/list", s.handleAPIAgentList)
	apiMux.HandleFunc("GET /api/template/list", s.handleAPITemplateList)
	apiMux.HandleFunc("GET /api/task/list", s.handleAPITaskList)
	apiMux.HandleFunc("POST /api/task/{name}/run", s.handleAPITaskRun)
	apiMux.HandleFunc("POST /api/task/{name}/toggle", s.handleAPITaskToggle)
	// Observability API (docs/specs/2026-07-31-observability-api.md): read-only
	// snapshot + SSE stream for external fleet watchers (The Den, leoterm, the
	// macOS app). Lives on apiMux so it inherits bearer auth unchanged — no new
	// auth mechanism, per the spec's Access section.
	apiMux.HandleFunc("GET /api/v1/state", s.handleAPIState)
	apiMux.HandleFunc("GET /api/v1/events", s.handleAPIEvents)
	// /api/* is the agent-facing surface: both tokens work there.
	protectedAPI := bearerAuthMiddleware([]string{s.apiToken, s.agentToken}, apiMux)

	// Path-prefix dispatcher: /api/* is routed through bearer auth to apiMux;
	// /login, /logout, and /static/* bypass session auth (otherwise the user
	// could never log in or load the login page's stylesheet). Everything
	// else (browser UI) is wrapped in sessionMiddleware, which accepts either
	// a valid session cookie or a Bearer token. /api/* lives on its own mux so
	// bearerAuthMiddleware can wrap it independently of the browser mux's
	// session middleware.
	protectedBrowser := sessionMiddleware(s.sessions, []string{s.apiToken}, mux)
	// The agent-messaging routes live on the browser mux but are called by
	// the in-agent MCP server (leo_send_message, leo_interrupt, key sends),
	// so they accept the agent token too. Everything else on this mux —
	// notably the config editor, which renders env values in full — stays
	// operator-only. See agentCallableBrowserPath.
	agentCallable := sessionMiddleware(s.sessions, []string{s.apiToken, s.agentToken}, mux)
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/static/"):
			mux.ServeHTTP(w, r)
		case r.URL.Path == "/login", r.URL.Path == "/logout":
			mux.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, "/api/"):
			protectedAPI.ServeHTTP(w, r)
		case agentCallableBrowserPath(r.URL.Path):
			agentCallable.ServeHTTP(w, r)
		default:
			protectedBrowser.ServeHTTP(w, r)
		}
	})

	// Every request — browser UI and API alike — passes through the Host +
	// Origin check. Defense in depth for the API: even with a valid token,
	// requests from a non-localhost browser context are rejected. A body-size
	// cap and baseline security headers sit above that.
	handler := hostOriginMiddleware(s.port, s.allowedHosts, root)
	handler = bodySizeMiddleware(maxRequestBodyBytes, handler)
	handler = securityHeadersMiddleware(handler)
	s.httpServer = &http.Server{
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	return s
}

// ListenAndServe starts the web server on the given address.
func (s *Server) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("web: listening on %s: %w", addr, err)
	}
	s.listener = ln

	go func() {
		if err := s.httpServer.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Printf("web UI server error: %v\n", err)
		}
	}()

	return nil
}

// Shutdown gracefully stops the web server.
func (s *Server) Shutdown() error {
	if s.httpServer == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.httpServer.Shutdown(ctx)
}

// Addr returns the listener address, or empty if not listening.
func (s *Server) Addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// loadConfig loads the current config from disk.
func (s *Server) loadConfig() (*config.Config, error) {
	return config.Load(s.configPath)
}

// loadHistory loads the task history store.
func (s *Server) loadHistory(cfg *config.Config) *history.Store {
	return history.NewStore(cfg.HomePath)
}

func (s *Server) parseTemplates() {
	funcMap := template.FuncMap{
		"statusColor": statusColor,
		"displayName": agent.DisplayName,
		"cronDesc":    describeCron,
		"relativeTime": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			d := time.Until(t)
			if d < 0 {
				d = time.Since(t)
				return formatDuration(d) + " ago"
			}
			return "in " + formatDuration(d)
		},
		"exitCodeClass": func(code int) string {
			if code == 0 {
				return "exit-success"
			}
			return "exit-failure"
		},
		"timeFormat": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			return t.Format("Jan 2 15:04")
		},
		"uptime": func(t time.Time) string {
			if t.IsZero() {
				return ""
			}
			return formatDuration(time.Since(t))
		},
		"kindName":    kindName,
		"optTypeName": optTypeName,
		"truncate": func(s string, maxLen int) string {
			runes := []rune(s)
			if len(runes) <= maxLen {
				return s
			}
			return string(runes[:maxLen]) + "\n... (truncated)"
		},
	}

	s.templates = template.Must(template.New("").Funcs(funcMap).ParseFS(content, "templates/*.html", "templates/**/*.html"))
}

// agentList returns the claude sub-agent names, refreshing at most once per
// minute. fetchAgentList shells out to `claude agents`, which can take up to
// 10s, so the shell-out itself must never run while agentMu is held.
//
// The caller that finds the cache stale claims the refresh by stamping
// agentsFetched immediately and releasing the lock before shelling out.
// Any concurrent caller that arrives during that window sees a fresh
// timestamp, so it returns the previous (stale) cache right away instead of
// blocking on the in-flight fetch or starting a second one.
func (s *Server) agentList() []string {
	s.agentMu.Lock()
	if time.Since(s.agentsFetched) <= time.Minute {
		cache := s.agentCache
		s.agentMu.Unlock()
		return cache
	}
	s.agentsFetched = time.Now()
	s.agentMu.Unlock()

	fresh := s.fetchAgentListFn()

	s.agentMu.Lock()
	if len(fresh) > 0 {
		s.agentCache = fresh
	}
	// agentsFetched was already stamped before the shell-out (above), so a
	// nil/empty result from a transient failure still resets the TTL clock
	// — it just keeps the previous (still-valid) cache instead of clobbering
	// it with nothing, and won't hammer the failing command every call.
	cache := s.agentCache
	s.agentMu.Unlock()

	return cache
}

// fetchAgentList runs `claude agents` and parses the agent names.
func (s *Server) fetchAgentList() []string {
	claudePath, err := exec.LookPath(claudeharness.Claude{}.Binary())
	if err != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, claudePath, "agents").Output()
	if err != nil {
		return nil
	}

	var agents []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, " · ") {
			name := strings.TrimSpace(strings.SplitN(line, " · ", 2)[0])
			if name != "" {
				agents = append(agents, name)
			}
		}
	}
	return agents
}

func statusColor(status string) string {
	switch status {
	case "running":
		return "status-running"
	case "restarting":
		return "status-restarting"
	case "stopped":
		return "status-stopped"
	default:
		return "status-disabled"
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	if hours == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd %dh", days, hours)
}
