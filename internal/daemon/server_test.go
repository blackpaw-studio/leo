package daemon

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func socketHTTPClient(sockPath string) *http.Client {
	return &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}
}

// tmpSockPath returns a short socket path under /tmp to stay within the
// 104-character macOS Unix socket path limit.
func tmpSockPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "leo-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, name)
}

func TestServerStartStop(t *testing.T) {
	sockPath := tmpSockPath(t, "d.sock")

	s := New(sockPath, "/tmp/leo.yaml", nil)

	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Verify socket file exists
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("socket file not found after Start(): %v", err)
	}

	// Hit the health endpoint
	client := socketHTTPClient(sockPath)
	resp, err := client.Get("http://localhost/health")
	if err != nil {
		t.Fatalf("GET /health error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var body Response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !body.OK {
		t.Errorf("expected OK=true, got OK=false (error: %s)", body.Error)
	}

	// Shutdown
	if err := s.Shutdown(); err != nil {
		t.Fatalf("Shutdown() error: %v", err)
	}

	// Verify socket file removed
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Errorf("socket file still exists after Shutdown()")
	}
}

func TestServerRemovesStaleSocket(t *testing.T) {
	sockPath := tmpSockPath(t, "d.sock")

	// Create a stale socket file
	f, err := os.Create(sockPath)
	if err != nil {
		t.Fatalf("creating stale socket file: %v", err)
	}
	f.Close()

	// Verify it exists before starting
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("stale socket file not found: %v", err)
	}

	s := New(sockPath, "/tmp/leo.yaml", nil)

	// Start should remove the stale file and bind successfully
	if err := s.Start(); err != nil {
		t.Fatalf("Start() with stale socket error: %v", err)
	}
	defer s.Shutdown()

	// Verify health endpoint works (confirms the server is running)
	client := socketHTTPClient(sockPath)
	resp, err := client.Get("http://localhost/health")
	if err != nil {
		t.Fatalf("GET /health error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var body Response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !body.OK {
		t.Errorf("expected OK=true, got OK=false (error: %s)", body.Error)
	}
}
