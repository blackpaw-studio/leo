package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// Server is an HTTP server listening on a Unix socket for daemon IPC.
type Server struct {
	sockPath   string
	configPath string
	httpServer *http.Server
	listener   net.Listener
}

// New creates a new daemon server.
func New(sockPath, configPath string) *Server {
	s := &Server{
		sockPath:   sockPath,
		configPath: configPath,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)

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
	go s.httpServer.Serve(ln) //nolint:errcheck

	return nil
}

// Shutdown gracefully stops the server and removes the socket file.
func (s *Server) Shutdown() error {
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, Response{OK: false, Error: msg})
}
