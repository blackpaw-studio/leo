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
	"sync/atomic"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/history"
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
}

// Server serves the Leo web UI over HTTP.
type Server struct {
	configPath    string
	processes     ProcessStateProvider
	scheduler     SchedulerProvider
	reloader      ConfigReloader
	agentSvc      AgentService
	leoPath       string
	templates     *template.Template
	httpServer    *http.Server
	listener      net.Listener
	agents        []string      // cached list of available claude agents
	restartNeeded atomic.Bool   // set when process-affecting config changes are saved; touched from concurrent handlers
	port          int           // port the listener is expected to bind on; used for Host/Origin checks
	apiToken      string        // bearer token required on /api/* routes; empty disables API
	allowedHosts  []string      // extra hosts permitted beyond loopback (e.g. LAN IPs)
	sessions      *sessionStore // in-memory browser sessions for cookie-based auth

	// Testability seam for exec.Command
	execCommand func(name string, args ...string) *exec.Cmd
}

// Options bundles the knobs the web server needs that aren't part of the
// provider interfaces. Zero values disable the corresponding surface:
//   - Port must match the listener port so Host/Origin checks pass.
//   - APIToken must be non-empty for /api/* routes to work. If empty, /api/*
//     responds 500 to avoid accidentally serving the API unauthenticated.
type Options struct {
	Port         int
	APIToken     string
	AllowedHosts []string
}

// New creates a new web UI server. agentSvc may be nil if agent spawning is not available.
func New(configPath string, processes ProcessStateProvider, scheduler SchedulerProvider, reloader ConfigReloader, agentSvc AgentService, opts Options) *Server {
	leoPath, err := exec.LookPath("leo")
	if err != nil {
		leoPath = "leo"
	}

	s := &Server{
		configPath:   configPath,
		processes:    processes,
		scheduler:    scheduler,
		reloader:     reloader,
		agentSvc:     agentSvc,
		leoPath:      leoPath,
		port:         opts.Port,
		apiToken:     opts.APIToken,
		allowedHosts: opts.AllowedHosts,
		execCommand:  exec.Command,
	}

	s.sessions = newSessionStore(sessionTTL)
	s.agents = s.fetchAgentList()
	s.parseTemplates()

	mux := http.NewServeMux()

	// Static assets
	staticFS, _ := fs.Sub(content, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// Login / logout (unprotected by sessionMiddleware; they are what grants the session).
	mux.HandleFunc("GET /login", s.loginHandler)
	mux.HandleFunc("POST /login", s.loginHandler)
	mux.HandleFunc("POST /logout", s.logoutHandler)

	// Full page
	mux.HandleFunc("GET /", s.handleDashboard)

	// Partials (htmx polling targets)
	mux.HandleFunc("GET /partials/status", s.handlePartialStatus)
	mux.HandleFunc("GET /partials/processes", s.handlePartialProcesses)
	mux.HandleFunc("GET /partials/tasks", s.handlePartialTasks)
	mux.HandleFunc("GET /partials/task/{name}/history", s.handlePartialTaskHistory)
	mux.HandleFunc("GET /partials/task/{name}/log", s.handleTaskRunLog)

	// Config sub-tab partials
	mux.HandleFunc("GET /partials/config/processes", s.handlePartialConfigProcesses)
	mux.HandleFunc("GET /partials/config/tasks", s.handlePartialConfigTasks)
	mux.HandleFunc("GET /partials/config/settings", s.handlePartialConfigSettings)

	// Utilities
	mux.HandleFunc("GET /web/cron/preview", s.handleCronPreview)

	// Task mutations
	mux.HandleFunc("POST /web/task/{name}/toggle", s.handleTaskToggle)
	mux.HandleFunc("POST /web/task/{name}/run", s.handleTaskRun)

	// Config mutations
	mux.HandleFunc("POST /web/config/reload", s.handleConfigReload)
	mux.HandleFunc("POST /web/config/defaults", s.handleConfigDefaults)
	mux.HandleFunc("POST /web/config/process/{name}", s.handleConfigProcess)
	mux.HandleFunc("POST /web/config/task/{name}", s.handleConfigTask)

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
	mux.HandleFunc("GET /partials/config/templates", s.handlePartialConfigTemplates)
	mux.HandleFunc("POST /web/config/template/{name}", s.handleConfigTemplate)
	mux.HandleFunc("POST /web/template/add", s.handleTemplateAdd)
	mux.HandleFunc("DELETE /web/template/{name}", s.handleTemplateDelete)

	// Service control
	mux.HandleFunc("POST /web/service/restart", s.handleServiceRestart)
	mux.HandleFunc("POST /web/process/{name}/interrupt", s.handleProcessInterrupt)
	mux.HandleFunc("POST /web/process/{name}/restart", s.handleProcessRestart)
	mux.HandleFunc("POST /web/process/{name}/send", s.handleProcessSendKeys)

	// Agent management (web UI)
	mux.HandleFunc("GET /partials/agents", s.handlePartialAgents)
	mux.HandleFunc("POST /web/agent/spawn", s.handleWebAgentSpawn)
	mux.HandleFunc("POST /web/agent/{name}/stop", s.handleWebAgentStop)

	// Agent + task management (JSON API — used by channel plugins and external
	// clients). Registered on a sub-mux so we can wrap /api/* in bearer auth
	// without affecting the browser-facing /web/* routes.
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("POST /api/agent/spawn", s.handleAPIAgentSpawn)
	apiMux.HandleFunc("POST /api/agent/stop", s.handleAPIAgentStop)
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
		"derefBool": func(b *bool) bool {
			if b == nil {
				return false
			}
			return *b
		},
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
