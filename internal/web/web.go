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
	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/history"
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
	Resume(name string) (agent.Record, error)
}

// SessionRuntimeProvider exposes the daemon's in-process session router
// operations the Sessions page needs: queue depth and reset. internal/daemon
// supplies the only real implementation (wired in via Options.SessionRuntime
// from StartWeb) and calls straight through to its sessionRouter — unlike
// the CLI, which runs as a separate process and must reach the router over
// the daemon's Unix-socket HTTP API (see internal/cli/session.go), the web
// UI is always served embedded inside the daemon process itself, so no
// socket round-trip is needed. This also means package web cannot import
// internal/daemon directly to call daemon.ResetSession/SessionDepth as a
// free function: internal/daemon/server.go already imports internal/web to
// embed this UI, and the reverse import would cycle. A nil
// SessionRuntimeProvider (e.g. in tests, or if the daemon integration is
// ever omitted) degrades the Sessions page to tmux-only status: queue depth
// stays unknown (-1) and reset only kills tmux + clears the stored session
// id, skipping the router notification — the same degrade path the CLI
// takes when daemon.IsRunning is false.
type SessionRuntimeProvider interface {
	// ResetSession drops any queued/in-flight invocations for session and
	// returns how many were cleared.
	ResetSession(session, reason string) int
	// SessionDepth returns the current queued + in-flight count.
	SessionDepth(session string) int
}

// Server serves the Leo web UI over HTTP.
type Server struct {
	configPath    string
	processes     ProcessStateProvider
	scheduler     SchedulerProvider
	reloader      ConfigReloader
	agentSvc      AgentService
	sessionRT     SessionRuntimeProvider // nil degrades Sessions page to tmux-only status; see SessionRuntimeProvider doc
	leoPath       string
	templates     *template.Template
	httpServer    *http.Server
	listener      net.Listener
	restartNeeded atomic.Bool   // set when process-affecting config changes are saved; touched from concurrent handlers
	port          int           // port the listener is expected to bind on; used for Host/Origin checks
	apiToken      string        // bearer token required on /api/* routes; empty disables API
	allowedHosts  []string      // extra hosts permitted beyond loopback (e.g. LAN IPs)
	sessions      *sessionStore // in-memory browser sessions for cookie-based auth

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

	// lookTmux is the testability seam for locating the tmux binary used by
	// the Sessions page's liveness check and reset action (see
	// handlers_sessions.go). Defaults to exec.LookPath("tmux"). Unlike
	// findTmuxPath (used elsewhere in this package), a failure here means
	// "tmux truly isn't available" rather than falling back to a bare
	// "tmux" string — tmuxSessionLive/handleSessionReset use the error to
	// skip the tmux call entirely. Tests stub this so the execCommand seam
	// is always reached regardless of whether the test runner has tmux
	// installed.
	lookTmux func() (string, error)

	// injectPrompt delivers a message into a tmux session via the readiness-
	// probing path (tmux.InjectPrompt). Tests replace this to verify the
	// resumed-agent message delivery path without requiring a real tmux session.
	injectPrompt func(ctx context.Context, session, body string) error
}

// Options bundles the knobs the web server needs that aren't part of the
// provider interfaces. Zero values disable the corresponding surface:
//   - Port must match the listener port so Host/Origin checks pass.
//   - APIToken must be non-empty for /api/* routes to work. If empty, /api/*
//     responds 500 to avoid accidentally serving the API unauthenticated.
type Options struct {
	Port           int
	APIToken       string
	AllowedHosts   []string
	SessionRuntime SessionRuntimeProvider // optional; nil degrades Sessions page to tmux-only status
	// LogPath is the absolute path to the service log, computed by
	// service.LogPathFor(homePath) at the layer that can import
	// internal/service (see internal/daemon/server.go's StartWeb). Optional;
	// empty disables the Service page's log tail (renders a "not
	// configured" message instead of guessing a path).
	LogPath string
}

// New creates a new web UI server. agentSvc may be nil if agent spawning is not available.
func New(configPath string, processes ProcessStateProvider, scheduler SchedulerProvider, reloader ConfigReloader, agentSvc AgentService, opts Options) *Server {
	leoPath, err := exec.LookPath("leo")
	if err != nil {
		leoPath = "leo"
	}

	s := &Server{
		configPath:     configPath,
		processes:      processes,
		scheduler:      scheduler,
		reloader:       reloader,
		agentSvc:       agentSvc,
		sessionRT:      opts.SessionRuntime,
		leoPath:        leoPath,
		port:           opts.Port,
		apiToken:       opts.APIToken,
		allowedHosts:   opts.AllowedHosts,
		serviceLogPath: opts.LogPath,
		execCommand:    exec.Command,
		lookTmux:       func() (string, error) { return exec.LookPath("tmux") },
	}
	s.fetchAgentListFn = s.fetchAgentList

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
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, "/tasks", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /tasks", s.handlePage("tasks", "Tasks", s.buildTasksData))
	mux.HandleFunc("GET /tasks/{name}", s.handleTaskEditPage)
	mux.HandleFunc("GET /agents", s.handlePage("agents", "Agents", s.buildAgentsData))
	mux.HandleFunc("GET /processes", s.handlePage("processes", "Processes", s.buildProcessesData))
	mux.HandleFunc("GET /processes/{name}", s.handleProcessEditPage)
	mux.HandleFunc("GET /sessions", s.handlePage("sessions", "Sessions", s.buildSessionsData))
	mux.HandleFunc("GET /config/defaults", s.handlePage("config_defaults", "Defaults", s.buildDefaultsData))
	mux.HandleFunc("GET /config/templates", s.handlePage("config_templates", "Templates", s.buildTemplatesData))
	mux.HandleFunc("GET /config/templates/{name}", s.handleTemplateEditPage)
	mux.HandleFunc("GET /config/settings", s.handlePage("config_settings", "Settings", s.buildSettingsData))
	mux.HandleFunc("GET /service", s.handlePage("service", "Service", s.buildServiceData))

	// Partials (htmx polling targets)
	mux.HandleFunc("GET /partials/status", s.handlePartialStatus)
	mux.HandleFunc("GET /partials/processes", s.handlePartialProcesses)
	mux.HandleFunc("GET /partials/task/{name}/history", s.handlePartialTaskHistory)
	mux.HandleFunc("GET /partials/task/{name}/log", s.handleTaskRunLog)

	// Utilities
	mux.HandleFunc("GET /web/cron/preview", s.handleCronPreview)

	// Task mutations
	mux.HandleFunc("POST /web/task/{name}/toggle", s.handleTaskToggle)
	mux.HandleFunc("POST /web/task/{name}/run", s.handleTaskRun)

	// Config mutations
	mux.HandleFunc("POST /web/config/reload", s.handleConfigReload)
	mux.HandleFunc("POST /web/config/defaults", s.handleConfigDefaultsSave)
	mux.HandleFunc("POST /web/config/process/{name}", s.handleConfigProcessSave)
	mux.HandleFunc("POST /web/config/task/{name}", s.handleConfigTaskSave)

	// Process CRUD
	mux.HandleFunc("POST /web/process/add", s.handleProcessAdd)
	mux.HandleFunc("DELETE /web/process/{name}", s.handleProcessDelete)

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

	// Settings page: Web UI + Remote client config, and remote-host CRUD —
	// full CRUD lives on one page (no separate edit page).
	mux.HandleFunc("POST /web/config/web", s.handleConfigWebSave)
	mux.HandleFunc("POST /web/config/client", s.handleConfigClientSave)
	mux.HandleFunc("POST /web/config/host/{name}", s.handleConfigHostSave)
	mux.HandleFunc("POST /web/host/add", s.handleHostAdd)
	mux.HandleFunc("DELETE /web/host/{name}", s.handleHostDelete)

	// Session config management — full CRUD lives on one page, same
	// one-page-no-separate-edit-page pattern as hosts above, plus a
	// runtime reset action (kills tmux, drops queued work, clears the
	// stored --resume session id).
	mux.HandleFunc("POST /web/config/session/{name}", s.handleConfigSessionSave)
	mux.HandleFunc("POST /web/session/add", s.handleSessionAdd)
	mux.HandleFunc("DELETE /web/session/{name}", s.handleSessionDelete)
	mux.HandleFunc("POST /web/session/{name}/reset", s.handleSessionReset)

	// Service control
	mux.HandleFunc("POST /web/service/restart", s.handleServiceRestart)
	mux.HandleFunc("GET /web/service/logtail", s.handleServiceLogTail)
	mux.HandleFunc("POST /web/process/{name}/interrupt", s.handleProcessInterrupt)
	mux.HandleFunc("POST /web/process/{name}/restart", s.handleProcessRestart)
	mux.HandleFunc("POST /web/process/{name}/send", s.handleProcessSendKeys)
	mux.HandleFunc("POST /web/process/{name}/message", s.handleProcessMessage)

	// Agent management (web UI). handlePartialAgents (agents.html re-render
	// after a rename) is invoked directly by handleWebAgentRename, not
	// routed — see handlers_agents.go.
	mux.HandleFunc("POST /web/agent/spawn", s.handleWebAgentSpawn)
	mux.HandleFunc("POST /web/agent/{name}/stop", s.handleWebAgentStop)
	mux.HandleFunc("POST /web/agent/{name}/rename", s.handleWebAgentRename)

	// Agent + task management (JSON API — used by channel plugins and external
	// clients). Registered on a sub-mux so we can wrap /api/* in bearer auth
	// without affecting the browser-facing /web/* routes.
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("POST /api/agent/spawn", s.handleAPIAgentSpawn)
	apiMux.HandleFunc("POST /api/agent/stop", s.handleAPIAgentStop)
	apiMux.HandleFunc("POST /api/agent/{name}/rename", s.handleAPIAgentRename)
	apiMux.HandleFunc("GET /api/agent/list", s.handleAPIAgentList)
	apiMux.HandleFunc("GET /api/template/list", s.handleAPITemplateList)
	apiMux.HandleFunc("GET /api/task/list", s.handleAPITaskList)
	apiMux.HandleFunc("POST /api/task/{name}/run", s.handleAPITaskRun)
	apiMux.HandleFunc("POST /api/task/{name}/toggle", s.handleAPITaskToggle)
	protectedAPI := bearerAuthMiddleware(s.apiToken, apiMux)

	// Path-prefix dispatcher: /api/* is routed through bearer auth to apiMux;
	// /login, /logout, and /static/* bypass session auth (otherwise the user
	// could never log in or load the login page's stylesheet). Everything
	// else (browser UI) is wrapped in sessionMiddleware, which accepts either
	// a valid session cookie or a Bearer token. We don't register "/api/" on
	// the main mux because that conflicts with "GET /" under the Go 1.22
	// ServeMux precedence rules.
	protectedBrowser := sessionMiddleware(s.sessions, s.apiToken, mux)
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/static/"):
			mux.ServeHTTP(w, r)
		case r.URL.Path == "/login", r.URL.Path == "/logout":
			mux.ServeHTTP(w, r)
		case strings.HasPrefix(r.URL.Path, "/api/"):
			protectedAPI.ServeHTTP(w, r)
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
		"kindName": kindName,
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
	claudePath, err := exec.LookPath("claude")
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
