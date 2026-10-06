package daemon

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDaemon answers /health on the workspace socket the way a real daemon
// does, with a readiness flag the test flips to stand in for agent restore
// finishing. Must be called from the test goroutine.
func fakeDaemon(t *testing.T, workDir string, ready *atomic.Bool) {
	t.Helper()
	ln, err := net.Listen("unix", SockPath(workDir))
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeData(w, http.StatusOK, healthData{Version: "v-test", Ready: ready.Load()})
	})}
	t.Cleanup(func() { _ = srv.Close() })
	go func() { _ = srv.Serve(ln) }()
}

// waitAsync runs WaitReady on its own goroutine so the test goroutine stays
// free to bring the fake daemon up (testing.T must only be used from there).
func waitAsync(workDir string, timeout time.Duration) <-chan error {
	done := make(chan error, 1)
	go func() { done <- WaitReady(context.Background(), workDir, timeout) }()
	return done
}

func readyFlag(v bool) *atomic.Bool {
	var b atomic.Bool
	b.Store(v)
	return &b
}

func awaitResult(t *testing.T, done <-chan error, within time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(within):
		t.Fatalf("WaitReady did not return within %s", within)
		return nil
	}
}

func TestWaitReady_ReturnsOnceDaemonBindsLate(t *testing.T) {
	workDir := tmpWorkDir(t)
	// The socket does not exist yet, exactly as right after launchctl
	// kickstart -k returns: the new daemon is still starting.
	done := waitAsync(workDir, 5*time.Second)
	time.Sleep(300 * time.Millisecond)
	fakeDaemon(t, workDir, readyFlag(true))

	if err := awaitResult(t, done, 5*time.Second); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
}

func TestWaitReady_PollsPastStaleSocketFile(t *testing.T) {
	workDir := tmpWorkDir(t)
	// A SIGKILLed daemon leaves its socket file behind with nobody
	// listening: dialing it is refused rather than ENOENT.
	stale, err := net.Listen("unix", SockPath(workDir))
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = stale.Close()
	if _, err := os.Stat(SockPath(workDir)); err != nil {
		t.Fatalf("stale socket file missing: %v", err)
	}

	done := waitAsync(workDir, 5*time.Second)
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("WaitReady returned against a stale socket: %v", err)
	default:
	}
	// The new daemon removes the stale file before binding (Server.Start).
	if err := os.Remove(SockPath(workDir)); err != nil {
		t.Fatalf("removing stale socket: %v", err)
	}
	fakeDaemon(t, workDir, readyFlag(true))

	if err := awaitResult(t, done, 5*time.Second); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
}

func TestWaitReady_HoldsUntilDaemonReportsReady(t *testing.T) {
	workDir := tmpWorkDir(t)
	ready := readyFlag(false)
	fakeDaemon(t, workDir, ready)

	done := waitAsync(workDir, 5*time.Second)
	// Healthy but still restoring agents: an early return here is exactly
	// the bug — /agents/stale would answer from a half-restored supervisor.
	select {
	case err := <-done:
		t.Fatalf("WaitReady returned while daemon was not ready: %v", err)
	case <-time.After(600 * time.Millisecond):
	}

	ready.Store(true)
	if err := awaitResult(t, done, 5*time.Second); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
}

func TestWaitReady_TimeoutWhileNotReadySaysSo(t *testing.T) {
	workDir := tmpWorkDir(t)
	fakeDaemon(t, workDir, readyFlag(false))

	err := WaitReady(context.Background(), workDir, 400*time.Millisecond)
	if err == nil {
		t.Fatal("WaitReady returned nil while daemon never became ready")
	}
	if !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("error %q should say the daemon answered but was not ready", err)
	}
}

func TestWaitReady_TimesOutWhenDaemonNeverComesUp(t *testing.T) {
	workDir := tmpWorkDir(t)

	start := time.Now()
	err := WaitReady(context.Background(), workDir, 400*time.Millisecond)
	if err == nil {
		t.Fatal("WaitReady returned nil with no daemon listening")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("WaitReady overran its timeout: %v", elapsed)
	}
}

func TestHealth_ReportsReadyOnlyAfterMarkReady(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "leo.sock"), "/nonexistent/leo.yaml", nil)
	s.SetObservability(nil, nil, nil, nil, "v-test")

	get := func() (int, string) {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
		return w.Code, w.Body.String()
	}

	// Liveness callers (IsRunning, SocketHealthy) only look at the status,
	// so /health stays 200 while agents are still being restored.
	code, body := get()
	if code != http.StatusOK || body != "{\"ok\":true,\"data\":{\"version\":\"v-test\",\"ready\":false}}\n" {
		t.Fatalf("before MarkReady: %d %s", code, body)
	}

	s.MarkReady()
	code, body = get()
	if code != http.StatusOK || body != "{\"ok\":true,\"data\":{\"version\":\"v-test\",\"ready\":true}}\n" {
		t.Fatalf("after MarkReady: %d %s", code, body)
	}
}
