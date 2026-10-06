package daemon

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

// serveHealth answers /health on the workspace socket, standing in for a
// daemon that has finished binding.
func serveHealth(t *testing.T, workDir string) {
	t.Helper()
	ln, err := net.Listen("unix", SockPath(workDir))
	if err != nil {
		t.Errorf("listening: %v", err)
		return
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	t.Cleanup(func() { _ = srv.Close() })
	go func() { _ = srv.Serve(ln) }()
}

func TestWaitHealthy_ReturnsOnceDaemonBindsLate(t *testing.T) {
	workDir := tmpWorkDir(t)
	// The socket does not exist yet, exactly as right after launchctl
	// kickstart -k returns: the new daemon is still starting.
	time.AfterFunc(300*time.Millisecond, func() { serveHealth(t, workDir) })

	if err := WaitHealthy(context.Background(), workDir, 5*time.Second); err != nil {
		t.Fatalf("WaitHealthy: %v", err)
	}
}

func TestWaitHealthy_TimesOutWhenDaemonNeverComesUp(t *testing.T) {
	workDir := tmpWorkDir(t)

	start := time.Now()
	err := WaitHealthy(context.Background(), workDir, 400*time.Millisecond)
	if err == nil {
		t.Fatal("WaitHealthy returned nil with no daemon listening")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("WaitHealthy overran its timeout: %v", elapsed)
	}
}
