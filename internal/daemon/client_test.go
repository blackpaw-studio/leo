package daemon

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// tmpWorkDir creates a short temp workspace dir under /tmp with the state/
// subdirectory pre-created. macOS limits Unix socket paths to 104 chars, so
// we must keep the path short.
func tmpWorkDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "leo-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0700); err != nil {
		t.Fatalf("creating state dir: %v", err)
	}
	return dir
}

// startServerAt starts a daemon server at SockPath(workDir) and registers
// cleanup to shut it down.
func startServerAt(t *testing.T, workDir string) *Server {
	t.Helper()
	sockPath := SockPath(workDir)
	s := New(sockPath, "/tmp/leo.yaml")
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	t.Cleanup(func() { s.Shutdown() }) //nolint:errcheck
	return s
}

func TestIsRunningNoSocket(t *testing.T) {
	workDir := tmpWorkDir(t)
	if IsRunning(workDir) {
		t.Error("expected IsRunning to return false when no daemon is running")
	}
}

func TestIsRunningWithDaemon(t *testing.T) {
	workDir := tmpWorkDir(t)
	startServerAt(t, workDir)

	if !IsRunning(workDir) {
		t.Error("expected IsRunning to return true when daemon is running")
	}
}

func TestSendHealthCheck(t *testing.T) {
	workDir := tmpWorkDir(t)
	startServerAt(t, workDir)

	resp, err := Send(workDir, http.MethodGet, "/health", nil)
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	if !resp.OK {
		t.Errorf("expected Response.OK=true, got false (error: %s)", resp.Error)
	}
}

func TestSendNoDaemon(t *testing.T) {
	workDir := tmpWorkDir(t)

	_, err := Send(workDir, http.MethodGet, "/health", nil)
	if err == nil {
		t.Error("expected Send to return an error when no daemon is running")
	}
}
