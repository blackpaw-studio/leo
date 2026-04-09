package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/cron"
)

// ProcessStateProvider returns the state of all supervised processes.
// This is implemented by service.Supervisor.
type ProcessStateProvider interface {
	States() map[string]ProcessStateInfo
}

// ProcessStateInfo is the daemon-facing view of a process state.
type ProcessStateInfo struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at"`
	Restarts  int       `json:"restarts"`
}

// Server is an HTTP server listening on a Unix socket for daemon IPC.
type Server struct {
	sockPath   string
	configPath string
	httpServer *http.Server
	listener   net.Listener
	scheduler  *cron.Scheduler
	processes  ProcessStateProvider
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

	s.httpServer = &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	return s
}

// Start binds the Unix socket and begins serving requests.
func (s *Server) Start() error {
	// Remove stale socket if present
	if _, err := os.Stat(s.sockPath); err == nil {
		os.Remove(s.sockPath)
	}

	ln, err := net.Listen("unix", s.sockPath)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", s.sockPath, err)
	}

	// Set socket permissions to owner-only
	if err := os.Chmod(s.sockPath, 0600); err != nil {
		ln.Close()
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

// Shutdown gracefully stops the server and removes the socket file.
func (s *Server) Shutdown() error {
	s.scheduler.Stop()

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

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Response{OK: true})
}

func (s *Server) handleProcessList(w http.ResponseWriter, r *http.Request) {
	if s.processes == nil {
		writeJSON(w, http.StatusOK, Response{OK: true, Data: json.RawMessage("[]")})
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
