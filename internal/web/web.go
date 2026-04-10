package web

import (
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"time"

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

// AgentSpawnRequest is the web-layer view of an agent spawn request.
type AgentSpawnRequest struct {
	Name       string
	ClaudeArgs []string
	WorkDir    string
	Env        map[string]string
	WebPort    string
}

// AgentManager can create and destroy ephemeral agents.
type AgentManager interface {
	SpawnAgent(spec AgentSpawnRequest) error
	StopAgent(name string) error
	EphemeralAgents() map[string]ProcessStateInfo
}

// Server serves the Leo web UI over HTTP.
type Server struct {
	configPath    string
	processes     ProcessStateProvider
	scheduler     SchedulerProvider
	reloader      ConfigReloader
	agentMgr      AgentManager
	leoPath       string
	templates     *template.Template
	httpServer    *http.Server
	listener      net.Listener
	agents        []string // cached list of available claude agents
	restartNeeded bool     // set when process-affecting config changes are saved

	// Testability seam for exec.Command
	execCommand func(name string, args ...string) *exec.Cmd
}

// New creates a new web UI server. agentMgr may be nil if agent spawning is not available.
func New(configPath string, processes ProcessStateProvider, scheduler SchedulerProvider, reloader ConfigReloader, agentMgr AgentManager) *Server {
	leoPath, err := exec.LookPath("leo")
	if err != nil {
		leoPath = "leo"
	}

	s := &Server{
		configPath:  configPath,
		processes:   processes,
		scheduler:   scheduler,
		reloader:    reloader,
		agentMgr:    agentMgr,
		leoPath:     leoPath,
		execCommand: exec.Command,
	}

	s.agents = s.fetchAgentList()
	s.parseTemplates()

	mux := http.NewServeMux()

	// Static assets
	staticFS, _ := fs.Sub(content, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

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

	// Service control
	mux.HandleFunc("POST /web/service/restart", s.handleServiceRestart)
	mux.HandleFunc("POST /web/process/{name}/interrupt", s.handleProcessInterrupt)
	mux.HandleFunc("POST /web/process/{name}/send", s.handleProcessSendKeys)

	// Agent management (web UI)
	mux.HandleFunc("GET /partials/agents", s.handlePartialAgents)
	mux.HandleFunc("POST /web/agent/spawn", s.handleWebAgentSpawn)
	mux.HandleFunc("POST /web/agent/{name}/stop", s.handleWebAgentStop)

	// Agent management (JSON API — used by Telegram plugin)
	mux.HandleFunc("POST /api/agent/spawn", s.handleAPIAgentSpawn)
	mux.HandleFunc("POST /api/agent/stop", s.handleAPIAgentStop)
	mux.HandleFunc("GET /api/agent/list", s.handleAPIAgentList)
	mux.HandleFunc("GET /api/template/list", s.handleAPITemplateList)

	// Task management (JSON API — used by Telegram plugin)
	mux.HandleFunc("GET /api/task/list", s.handleAPITaskList)
	mux.HandleFunc("POST /api/task/{name}/run", s.handleAPITaskRun)
	mux.HandleFunc("POST /api/task/{name}/toggle", s.handleAPITaskToggle)

	s.httpServer = &http.Server{
		Handler:      mux,
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
		if err := s.httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
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
