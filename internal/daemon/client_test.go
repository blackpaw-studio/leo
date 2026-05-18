package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	s := New(sockPath, "/tmp/leo.yaml", nil)
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

	resp, err := Send(context.Background(), workDir, http.MethodGet, "/health", nil)
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	if !resp.OK {
		t.Errorf("expected Response.OK=true, got false (error: %s)", resp.Error)
	}
}

func TestSendNoDaemon(t *testing.T) {
	workDir := tmpWorkDir(t)

	_, err := Send(context.Background(), workDir, http.MethodGet, "/health", nil)
	if err == nil {
		t.Error("expected Send to return an error when no daemon is running")
	}
}

func TestSendWithBody(t *testing.T) {
	workDir := tmpWorkDir(t)

	// Write a valid config so handlers work
	cfgPath := filepath.Join(workDir, "leo.yaml")
	cfgYAML := `
agent:
  name: test-agent
  workspace: /tmp/test-workspace
defaults:
  model: sonnet
  max_turns: 10
tasks:
  heartbeat:
    schedule: "0 * * * *"
    prompt_file: heartbeat.md
    enabled: true
`
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0600); err != nil {
		t.Fatalf("writing config: %v", err)
	}

	sockPath := SockPath(workDir)
	s := New(sockPath, cfgPath, nil)
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	t.Cleanup(func() { s.Shutdown() }) //nolint:errcheck

	body := TaskNameRequest{Name: "heartbeat"}
	resp, err := Send(context.Background(), workDir, http.MethodPost, "/task/remove", body)
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	if !resp.OK {
		t.Errorf("expected OK=true, got false (error: %s)", resp.Error)
	}
}

func TestSockPath(t *testing.T) {
	got := SockPath("/home/user/.leo")
	want := filepath.Join("/home/user/.leo", "state", "leo.sock")
	if got != want {
		t.Errorf("SockPath() = %q, want %q", got, want)
	}
}

func TestClientEnqueueTask(t *testing.T) {
	h := http.NewServeMux()
	h.HandleFunc("POST /task/enqueue", func(w http.ResponseWriter, r *http.Request) {
		var got taskEnqueueReq
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got.Task != "t1" || got.Prompt != "p" || got.QueueMax != 4 {
			t.Errorf("body wrong: %+v", got)
		}
		writeJSON(w, http.StatusOK, taskEnqueueResp{Accepted: true, InvocationID: "id-1"})
	})
	ts := httptest.NewServer(h)
	defer ts.Close()
	inv, err := EnqueueTaskHTTP(context.Background(), ts.URL, EnqueueRequest{
		Session: "s", Task: "t1", Prompt: "p", QueueMax: 4, Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if !inv.Accepted || inv.InvocationID != "id-1" {
		t.Fatalf("response wrong: %+v", inv)
	}
}

func TestClientAwaitTask(t *testing.T) {
	h := http.NewServeMux()
	h.HandleFunc("GET /task/await", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "invocation_id=id-1") {
			t.Errorf("query missing id: %s", r.URL.RawQuery)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "session_id": "cs1", "final_message": "done",
		})
	})
	ts := httptest.NewServer(h)
	defer ts.Close()
	res, err := AwaitTaskHTTP(context.Background(), ts.URL, "id-1")
	if err != nil {
		t.Fatalf("await: %v", err)
	}
	if !res.OK || res.SessionID != "cs1" || res.FinalMessage != "done" {
		t.Fatalf("result: %+v", res)
	}
}

func TestClientReportTask(t *testing.T) {
	var received taskReportReq
	h := http.NewServeMux()
	h.HandleFunc("POST /task/report", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&received)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	ts := httptest.NewServer(h)
	defer ts.Close()
	if err := ReportTaskHTTP(context.Background(), ts.URL, "id-xyz", "csid", "all done", "leo-session-foo"); err != nil {
		t.Fatalf("report: %v", err)
	}
	if received.InvocationID != "id-xyz" || received.SessionID != "csid" || received.FinalMessage != "all done" {
		t.Fatalf("server received: %+v", received)
	}
}
