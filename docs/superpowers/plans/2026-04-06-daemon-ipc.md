# Daemon IPC Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Unix socket HTTP server to the Leo daemon so Claude Code can delegate privileged operations (cron management, task config) through normal `leo` CLI commands.

**Architecture:** The daemon starts an `net/http` server on `{workspace}/state/leo.sock` alongside the tmux-based Claude session. CLI commands (`leo cron *`, `leo task *`) detect the socket and forward requests. If no daemon is running, commands execute locally as before.

**Tech Stack:** Go stdlib (`net/http`, `net`, `encoding/json`), Unix domain sockets

---

### Task 1: Daemon Types

**Files:**
- Create: `internal/daemon/types.go`
- Test: `internal/daemon/types_test.go`

- [ ] **Step 1: Write the failing test**

```go
package daemon

import (
	"encoding/json"
	"testing"
)

func TestResponseMarshal(t *testing.T) {
	resp := Response{OK: true, Data: map[string]string{"status": "installed"}}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" {
		t.Error("expected non-empty JSON")
	}

	var decoded Response
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.OK {
		t.Error("expected OK=true")
	}
}

func TestResponseErrorMarshal(t *testing.T) {
	resp := Response{OK: false, Error: "something failed"}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Response
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.OK {
		t.Error("expected OK=false")
	}
	if decoded.Error != "something failed" {
		t.Errorf("error = %q, want %q", decoded.Error, "something failed")
	}
}

func TestTaskAddRequestMarshal(t *testing.T) {
	req := TaskAddRequest{
		Name:       "heartbeat",
		Schedule:   "0 9 * * *",
		PromptFile: "HEARTBEAT.md",
		Model:      "sonnet",
		Enabled:    true,
		Silent:     true,
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var decoded TaskAddRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Name != "heartbeat" {
		t.Errorf("name = %q, want %q", decoded.Name, "heartbeat")
	}
	if !decoded.Silent {
		t.Error("expected Silent=true")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -race ./internal/daemon/ -v`
Expected: FAIL — package doesn't exist yet

- [ ] **Step 3: Write the types**

```go
package daemon

import "encoding/json"

// Response is the standard envelope for all daemon API responses.
type Response struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

// CronRequest is the body for POST /cron/install and /cron/remove.
type CronRequest struct {
	ConfigPath string `json:"config_path"`
}

// TaskAddRequest is the body for POST /task/add.
type TaskAddRequest struct {
	Name       string `json:"name"`
	Schedule   string `json:"schedule"`
	PromptFile string `json:"prompt_file"`
	Model      string `json:"model,omitempty"`
	MaxTurns   int    `json:"max_turns,omitempty"`
	Topic      string `json:"topic,omitempty"`
	Silent     bool   `json:"silent,omitempty"`
	Enabled    bool   `json:"enabled"`
}

// TaskNameRequest is the body for POST /task/remove, /task/enable, /task/disable.
type TaskNameRequest struct {
	Name string `json:"name"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race ./internal/daemon/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/types.go internal/daemon/types_test.go
git commit -m "feat: add daemon IPC request/response types"
```

---

### Task 2: Daemon Server

**Files:**
- Create: `internal/daemon/server.go`
- Test: `internal/daemon/server_test.go`

- [ ] **Step 1: Write the failing test**

```go
package daemon

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServerStartStop(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "leo.sock")

	srv := New(sockPath, filepath.Join(dir, "leo.yaml"))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	// Socket file should exist
	if _, err := os.Stat(sockPath); err != nil {
		t.Fatalf("socket file not created: %v", err)
	}

	// Health endpoint should respond
	client := socketHTTPClient(sockPath)
	resp, err := client.Get("http://daemon/health")
	if err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var body Response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if !body.OK {
		t.Error("expected OK=true from health check")
	}

	// Shutdown should clean up socket
	if err := srv.Shutdown(); err != nil {
		t.Fatalf("Shutdown() error: %v", err)
	}

	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Error("socket file should be removed after shutdown")
	}
}

func TestServerRemovesStaleSocket(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "leo.sock")

	// Create a stale socket file
	os.WriteFile(sockPath, []byte("stale"), 0600)

	srv := New(sockPath, filepath.Join(dir, "leo.yaml"))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start() should remove stale socket: %v", err)
	}
	defer srv.Shutdown()

	// Should be listening
	client := socketHTTPClient(sockPath)
	resp, err := client.Get("http://daemon/health")
	if err != nil {
		t.Fatalf("health check failed after stale cleanup: %v", err)
	}
	resp.Body.Close()
}

func socketHTTPClient(sockPath string) *http.Client {
	return &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx __context.Context__, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}
}
```

Note: Replace `__context.Context__` with the proper `context.Context` import — the underscores are plan escaping. Add `"context"` to imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -race ./internal/daemon/ -run TestServer -v`
Expected: FAIL — `New` function not defined

- [ ] **Step 3: Write the server**

```go
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

	go s.httpServer.Serve(ln)

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
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, Response{OK: false, Error: msg})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race ./internal/daemon/ -run TestServer -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/server.go internal/daemon/server_test.go
git commit -m "feat: add daemon HTTP server with Unix socket and health endpoint"
```

---

### Task 3: Cron Handlers

**Files:**
- Modify: `internal/daemon/server.go` (register routes)
- Create: `internal/daemon/handlers.go`
- Test: `internal/daemon/handlers_test.go`

- [ ] **Step 1: Write the failing tests**

```go
package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/blackpaw-studio/leo/internal/cron"
)

func setupTestServer(t *testing.T, configYAML string) (*Server, *http.Client, string) {
	t.Helper()
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "leo.sock")
	cfgPath := filepath.Join(dir, "leo.yaml")
	os.WriteFile(cfgPath, []byte(configYAML), 0600)

	// Create workspace structure the config references
	os.MkdirAll(filepath.Join(dir, "state"), 0750)

	srv := New(sockPath, cfgPath)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	t.Cleanup(func() { srv.Shutdown() })

	client := socketHTTPClient(sockPath)
	return srv, client, dir
}

const testConfig = `agent:
  name: test-agent
  workspace: %s
telegram:
  bot_token: "fake-token"
  chat_id: "12345"
defaults:
  model: sonnet
  max_turns: 15
tasks:
  heartbeat:
    schedule: "0 9 * * *"
    prompt_file: HEARTBEAT.md
    enabled: true
`

func TestHandleCronInstall(t *testing.T) {
	// Stub crontab so we don't touch real system
	origRead := cron.ExportReadCrontab()
	origWrite := cron.ExportWriteCrontab()
	defer func() {
		cron.SetReadCrontab(origRead)
		cron.SetWriteCrontab(origWrite)
	}()

	var written string
	cron.SetReadCrontab(func() (string, error) { return "", nil })
	cron.SetWriteCrontab(func(content string) error { written = content; return nil })

	cfg := fmt.Sprintf(testConfig, t.TempDir())
	_, client, dir := setupTestServer(t, cfg)

	// Create prompt file
	os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("check in"), 0600)

	body, _ := json.Marshal(CronRequest{ConfigPath: filepath.Join(dir, "leo.yaml")})
	resp, err := client.Post("http://daemon/cron/install", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /cron/install: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	if written == "" {
		t.Error("crontab was not written")
	}
}

func TestHandleCronList(t *testing.T) {
	origRead := cron.ExportReadCrontab()
	defer cron.SetReadCrontab(origRead)

	cron.SetReadCrontab(func() (string, error) {
		return "# === LEO:test-agent — DO NOT EDIT ===\n# leo:test-agent:heartbeat\n0 9 * * * leo run heartbeat\n# === END LEO:test-agent ===\n", nil
	})

	cfg := fmt.Sprintf(testConfig, t.TempDir())
	_, client, _ := setupTestServer(t, cfg)

	resp, err := client.Get("http://daemon/cron/list")
	if err != nil {
		t.Fatalf("GET /cron/list: %v", err)
	}
	defer resp.Body.Close()

	var body Response
	json.NewDecoder(resp.Body).Decode(&body)
	if !body.OK {
		t.Error("expected OK=true")
	}
}
```

Note: The cron package uses package-level vars `readCrontab` and `writeCrontab` for testability. We need to export setter/getter functions. See Step 3b.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -race ./internal/daemon/ -run TestHandleCron -v`
Expected: FAIL — handler functions not defined, cron export functions not defined

- [ ] **Step 3a: Add cron testability exports**

Add to `internal/cron/cron.go` after line 157:

```go
// ExportReadCrontab returns the current readCrontab function (for testing).
func ExportReadCrontab() func() (string, error) { return readCrontab }

// SetReadCrontab sets the readCrontab function (for testing).
func SetReadCrontab(fn func() (string, error)) { readCrontab = fn }

// ExportWriteCrontab returns the current writeCrontab function (for testing).
func ExportWriteCrontab() func(string) error { return writeCrontab }

// SetWriteCrontab sets the writeCrontab function (for testing).
func SetWriteCrontab(fn func(string) error) { writeCrontab = fn }
```

- [ ] **Step 3b: Write the cron handlers**

Create `internal/daemon/handlers.go`:

```go
package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/cron"
)

func (s *Server) handleCronInstall(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("loading config: %v", err))
		return
	}

	leoPath, err := exec.LookPath("leo")
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("finding leo binary: %v", err))
		return
	}

	if err := cron.Install(cfg, leoPath); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("installing cron: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, Response{OK: true})
}

func (s *Server) handleCronRemove(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("loading config: %v", err))
		return
	}

	if err := cron.Remove(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("removing cron: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, Response{OK: true})
}

func (s *Server) handleCronList(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("loading config: %v", err))
		return
	}

	block, err := cron.List(cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("listing cron: %v", err))
		return
	}

	data, _ := json.Marshal(map[string]string{"entries": block})
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}
```

- [ ] **Step 3c: Register cron routes in server.go**

Add these lines to the `New()` function in `server.go`, after the health route:

```go
	mux.HandleFunc("POST /cron/install", s.handleCronInstall)
	mux.HandleFunc("POST /cron/remove", s.handleCronRemove)
	mux.HandleFunc("GET /cron/list", s.handleCronList)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race ./internal/daemon/ -run TestHandleCron -v`
Expected: PASS

Also run existing cron tests to verify no regressions:
Run: `go test -race ./internal/cron/ -v`

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/handlers.go internal/daemon/server.go internal/cron/cron.go
git commit -m "feat: add cron install/remove/list handlers to daemon server"
```

---

### Task 4: Task Handlers

**Files:**
- Modify: `internal/daemon/handlers.go`
- Modify: `internal/daemon/server.go` (register routes)
- Test: `internal/daemon/handlers_test.go` (append)

- [ ] **Step 1: Write the failing tests**

Append to `internal/daemon/handlers_test.go`:

```go
func TestHandleTaskAdd(t *testing.T) {
	dir := t.TempDir()
	cfg := fmt.Sprintf(testConfig, dir)
	_, client, _ := setupTestServer(t, cfg)

	// Create prompt file the new task references
	os.WriteFile(filepath.Join(dir, "NEWS.md"), []byte("get news"), 0600)

	req := TaskAddRequest{
		Name:       "news",
		Schedule:   "0 7 * * *",
		PromptFile: "NEWS.md",
		Model:      "sonnet",
		Enabled:    true,
	}
	body, _ := json.Marshal(req)
	resp, err := client.Post("http://daemon/task/add", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /task/add: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errResp Response
		json.NewDecoder(resp.Body).Decode(&errResp)
		t.Fatalf("status = %d, error = %q", resp.StatusCode, errResp.Error)
	}

	// Verify config was updated
	updated, err := config.Load(filepath.Join(dir, "leo.yaml"))
	if err != nil {
		t.Fatalf("loading updated config: %v", err)
	}
	if _, ok := updated.Tasks["news"]; !ok {
		t.Error("task 'news' not found in updated config")
	}
}

func TestHandleTaskRemove(t *testing.T) {
	dir := t.TempDir()
	cfg := fmt.Sprintf(testConfig, dir)
	_, client, _ := setupTestServer(t, cfg)

	req := TaskNameRequest{Name: "heartbeat"}
	body, _ := json.Marshal(req)
	resp, err := client.Post("http://daemon/task/remove", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /task/remove: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	// Verify config was updated
	updated, err := config.Load(filepath.Join(dir, "leo.yaml"))
	if err != nil {
		t.Fatalf("loading updated config: %v", err)
	}
	if _, ok := updated.Tasks["heartbeat"]; ok {
		t.Error("task 'heartbeat' should have been removed")
	}
}

func TestHandleTaskEnable(t *testing.T) {
	// Use a config with a disabled task
	dir := t.TempDir()
	cfgYAML := fmt.Sprintf(`agent:
  name: test-agent
  workspace: %s
telegram:
  bot_token: "fake-token"
  chat_id: "12345"
defaults:
  model: sonnet
  max_turns: 15
tasks:
  heartbeat:
    schedule: "0 9 * * *"
    prompt_file: HEARTBEAT.md
    enabled: false
`, dir)
	_, client, _ := setupTestServer(t, cfgYAML)

	req := TaskNameRequest{Name: "heartbeat"}
	body, _ := json.Marshal(req)
	resp, err := client.Post("http://daemon/task/enable", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /task/enable: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	updated, err := config.Load(filepath.Join(dir, "leo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Tasks["heartbeat"].Enabled {
		t.Error("task should be enabled")
	}
}

func TestHandleTaskDisable(t *testing.T) {
	dir := t.TempDir()
	cfg := fmt.Sprintf(testConfig, dir)
	_, client, _ := setupTestServer(t, cfg)

	req := TaskNameRequest{Name: "heartbeat"}
	body, _ := json.Marshal(req)
	resp, err := client.Post("http://daemon/task/disable", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /task/disable: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	updated, err := config.Load(filepath.Join(dir, "leo.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Tasks["heartbeat"].Enabled {
		t.Error("task should be disabled")
	}
}

func TestHandleTaskList(t *testing.T) {
	dir := t.TempDir()
	cfg := fmt.Sprintf(testConfig, dir)
	_, client, _ := setupTestServer(t, cfg)

	resp, err := client.Get("http://daemon/task/list")
	if err != nil {
		t.Fatalf("GET /task/list: %v", err)
	}
	defer resp.Body.Close()

	var body Response
	json.NewDecoder(resp.Body).Decode(&body)
	if !body.OK {
		t.Errorf("expected OK=true, error=%q", body.Error)
	}
}

func TestHandleTaskRemoveNotFound(t *testing.T) {
	dir := t.TempDir()
	cfg := fmt.Sprintf(testConfig, dir)
	_, client, _ := setupTestServer(t, cfg)

	req := TaskNameRequest{Name: "nonexistent"}
	body, _ := json.Marshal(req)
	resp, err := client.Post("http://daemon/task/remove", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -race ./internal/daemon/ -run TestHandleTask -v`
Expected: FAIL — handler functions not defined

- [ ] **Step 3a: Write the task handlers**

Append to `internal/daemon/handlers.go`:

```go
func (s *Server) handleTaskAdd(w http.ResponseWriter, r *http.Request) {
	var req TaskAddRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request: %v", err))
		return
	}

	if req.Name == "" || req.Schedule == "" || req.PromptFile == "" {
		writeError(w, http.StatusBadRequest, "name, schedule, and prompt_file are required")
		return
	}

	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("loading config: %v", err))
		return
	}

	if cfg.Tasks == nil {
		cfg.Tasks = make(map[string]config.TaskConfig)
	}

	cfg.Tasks[req.Name] = config.TaskConfig{
		Schedule:   req.Schedule,
		PromptFile: req.PromptFile,
		Model:      req.Model,
		MaxTurns:   req.MaxTurns,
		Topic:      req.Topic,
		Silent:     req.Silent,
		Enabled:    req.Enabled,
	}

	if err := config.Save(s.configPath, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("saving config: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, Response{OK: true})
}

func (s *Server) handleTaskRemove(w http.ResponseWriter, r *http.Request) {
	var req TaskNameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request: %v", err))
		return
	}

	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("loading config: %v", err))
		return
	}

	if _, ok := cfg.Tasks[req.Name]; !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("task %q not found", req.Name))
		return
	}

	delete(cfg.Tasks, req.Name)

	if err := config.Save(s.configPath, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("saving config: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, Response{OK: true})
}

func (s *Server) handleTaskEnable(w http.ResponseWriter, r *http.Request) {
	s.setTaskEnabled(w, r, true)
}

func (s *Server) handleTaskDisable(w http.ResponseWriter, r *http.Request) {
	s.setTaskEnabled(w, r, false)
}

func (s *Server) setTaskEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	var req TaskNameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid request: %v", err))
		return
	}

	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("loading config: %v", err))
		return
	}

	task, ok := cfg.Tasks[req.Name]
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("task %q not found", req.Name))
		return
	}

	task.Enabled = enabled
	cfg.Tasks[req.Name] = task

	if err := config.Save(s.configPath, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("saving config: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, Response{OK: true})
}

func (s *Server) handleTaskList(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("loading config: %v", err))
		return
	}

	data, _ := json.Marshal(cfg.Tasks)
	writeJSON(w, http.StatusOK, Response{OK: true, Data: data})
}
```

- [ ] **Step 3b: Register task routes in server.go**

Add these lines to the `New()` function in `server.go`, after the cron routes:

```go
	mux.HandleFunc("POST /task/add", s.handleTaskAdd)
	mux.HandleFunc("POST /task/remove", s.handleTaskRemove)
	mux.HandleFunc("POST /task/enable", s.handleTaskEnable)
	mux.HandleFunc("POST /task/disable", s.handleTaskDisable)
	mux.HandleFunc("GET /task/list", s.handleTaskList)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/daemon/ -v`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/handlers.go internal/daemon/server.go internal/daemon/handlers_test.go
git commit -m "feat: add task add/remove/enable/disable/list handlers to daemon"
```

---

### Task 5: Daemon Client

**Files:**
- Create: `internal/daemon/client.go`
- Test: `internal/daemon/client_test.go`

- [ ] **Step 1: Write the failing test**

```go
package daemon

import (
	"path/filepath"
	"testing"
)

func TestIsRunning(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "state", "leo.sock")

	// No socket — should return false
	if IsRunning(dir) {
		t.Error("IsRunning should return false when no socket exists")
	}

	// Start a server
	os.MkdirAll(filepath.Join(dir, "state"), 0750)
	srv := New(sockPath, filepath.Join(dir, "leo.yaml"))
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown()

	// Now should return true
	if !IsRunning(dir) {
		t.Error("IsRunning should return true when daemon is listening")
	}
}

func TestSend(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "state", "leo.sock")
	os.MkdirAll(filepath.Join(dir, "state"), 0750)

	srv := New(sockPath, filepath.Join(dir, "leo.yaml"))
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown()

	resp, err := Send(dir, "GET", "/health", nil)
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	if !resp.OK {
		t.Error("expected OK=true")
	}
}

func TestSendNoDaemon(t *testing.T) {
	dir := t.TempDir()
	_, err := Send(dir, "GET", "/health", nil)
	if err == nil {
		t.Error("Send should fail when no daemon is running")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -race ./internal/daemon/ -run "TestIsRunning|TestSend" -v`
Expected: FAIL — `IsRunning` and `Send` not defined

- [ ] **Step 3: Write the client**

```go
package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

// SockPath returns the daemon socket path for a workspace.
func SockPath(workDir string) string {
	return filepath.Join(workDir, "state", "leo.sock")
}

// IsRunning checks if a daemon is listening on the workspace socket.
func IsRunning(workDir string) bool {
	sockPath := SockPath(workDir)
	client := newUnixClient(sockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "http://daemon/health", nil)
	if err != nil {
		return false
	}

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Send sends a request to the daemon and returns the response.
func Send(workDir, method, path string, body any) (*Response, error) {
	sockPath := SockPath(workDir)
	client := newUnixClient(sockPath)

	var reqBody *bytes.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshaling request: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	var httpReq *http.Request
	var err error
	if reqBody != nil {
		httpReq, err = http.NewRequest(method, "http://daemon"+path, reqBody)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
	} else {
		httpReq, err = http.NewRequest(method, "http://daemon"+path, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("connecting to daemon: %w", err)
	}
	defer resp.Body.Close()

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return &result, nil
}

func newUnixClient(sockPath string) *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race ./internal/daemon/ -v`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/client.go internal/daemon/client_test.go
git commit -m "feat: add daemon client for CLI passthrough"
```

---

### Task 6: CLI Passthrough — Cron Commands

**Files:**
- Modify: `internal/cli/cron.go`

- [ ] **Step 1: Modify cron install command to check for daemon**

Replace the `RunE` in `newCronInstallCmd()` (lines 29-47 of `internal/cli/cron.go`):

```go
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if daemon.IsRunning(cfg.Agent.Workspace) {
				resp, err := daemon.Send(cfg.Agent.Workspace, "POST", "/cron/install", nil)
				if err != nil {
					return fmt.Errorf("daemon request failed: %w", err)
				}
				if !resp.OK {
					return fmt.Errorf("daemon error: %s", resp.Error)
				}
				success.Println("Cron entries installed (via daemon).")
				return nil
			}

			leoPath, err := leoExecutablePath()
			if err != nil {
				return fmt.Errorf("finding leo binary: %w", err)
			}

			if err := cron.Install(cfg, leoPath); err != nil {
				return err
			}

			success.Println("Cron entries installed.")
			return nil
		},
```

- [ ] **Step 2: Modify cron remove command**

Replace the `RunE` in `newCronRemoveCmd()` (lines 54-67):

```go
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if daemon.IsRunning(cfg.Agent.Workspace) {
				resp, err := daemon.Send(cfg.Agent.Workspace, "POST", "/cron/remove", nil)
				if err != nil {
					return fmt.Errorf("daemon request failed: %w", err)
				}
				if !resp.OK {
					return fmt.Errorf("daemon error: %s", resp.Error)
				}
				success.Println("Cron entries removed (via daemon).")
				return nil
			}

			if err := cron.Remove(cfg); err != nil {
				return err
			}

			success.Println("Cron entries removed.")
			return nil
		},
```

- [ ] **Step 3: Modify cron list command**

Replace the `RunE` in `newCronListCmd()` (lines 74-93):

```go
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if daemon.IsRunning(cfg.Agent.Workspace) {
				resp, err := daemon.Send(cfg.Agent.Workspace, "GET", "/cron/list", nil)
				if err != nil {
					return fmt.Errorf("daemon request failed: %w", err)
				}
				if !resp.OK {
					return fmt.Errorf("daemon error: %s", resp.Error)
				}
				var data struct {
					Entries string `json:"entries"`
				}
				json.Unmarshal(resp.Data, &data)
				if data.Entries == "" {
					warn.Println("No leo cron entries found.")
				} else {
					fmt.Println(data.Entries)
				}
				return nil
			}

			block, err := cron.List(cfg)
			if err != nil {
				return err
			}

			if block == "" {
				warn.Println("No leo cron entries found.")
				return nil
			}

			fmt.Println(block)
			return nil
		},
```

- [ ] **Step 4: Add daemon import to cron.go**

Add to the imports:

```go
	"encoding/json"

	"github.com/blackpaw-studio/leo/internal/daemon"
```

- [ ] **Step 5: Run all tests to verify no regressions**

Run: `go test -race ./...`
Expected: ALL PASS

- [ ] **Step 6: Commit**

```bash
git add internal/cli/cron.go
git commit -m "feat: add daemon passthrough to cron CLI commands"
```

---

### Task 7: CLI Passthrough — Task Commands

**Files:**
- Modify: `internal/cli/task.go`

- [ ] **Step 1: Modify task remove command**

Replace the `RunE` in `newTaskRemoveCmd()` (lines 114-138 of `internal/cli/task.go`):

```go
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			name := args[0]

			if daemon.IsRunning(cfg.Agent.Workspace) {
				resp, err := daemon.Send(cfg.Agent.Workspace, "POST", "/task/remove",
					daemon.TaskNameRequest{Name: name})
				if err != nil {
					return fmt.Errorf("daemon request failed: %w", err)
				}
				if !resp.OK {
					return fmt.Errorf("daemon error: %s", resp.Error)
				}
				success.Printf("Task %q removed (via daemon).\n", name)
				return nil
			}

			if _, ok := cfg.Tasks[name]; !ok {
				return fmt.Errorf("task %q not found", name)
			}

			delete(cfg.Tasks, name)

			cfgPath, err := configPath()
			if err != nil {
				return err
			}

			if err := config.Save(cfgPath, cfg); err != nil {
				return err
			}

			success.Printf("Task %q removed.\n", name)
			return nil
		},
```

- [ ] **Step 2: Modify setTaskEnabled to support daemon passthrough**

Replace `setTaskEnabled` function (lines 164-193):

```go
func setTaskEnabled(name string, enabled bool) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	if daemon.IsRunning(cfg.Agent.Workspace) {
		path := "/task/enable"
		if !enabled {
			path = "/task/disable"
		}
		resp, err := daemon.Send(cfg.Agent.Workspace, "POST", path,
			daemon.TaskNameRequest{Name: name})
		if err != nil {
			return fmt.Errorf("daemon request failed: %w", err)
		}
		if !resp.OK {
			return fmt.Errorf("daemon error: %s", resp.Error)
		}
		action := "enabled"
		if !enabled {
			action = "disabled"
		}
		success.Printf("Task %q %s (via daemon).\n", name, action)
		return nil
	}

	task, ok := cfg.Tasks[name]
	if !ok {
		return fmt.Errorf("task %q not found", name)
	}

	task.Enabled = enabled
	cfg.Tasks[name] = task

	cfgPath, err := configPath()
	if err != nil {
		return err
	}

	if err := config.Save(cfgPath, cfg); err != nil {
		return err
	}

	action := "enabled"
	if !enabled {
		action = "disabled"
	}
	success.Printf("Task %q %s.\n", name, action)
	return nil
}
```

- [ ] **Step 3: Modify task list command**

Replace the `RunE` in `newTaskListCmd()` (lines 34-57):

```go
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if daemon.IsRunning(cfg.Agent.Workspace) {
				resp, err := daemon.Send(cfg.Agent.Workspace, "GET", "/task/list", nil)
				if err != nil {
					return fmt.Errorf("daemon request failed: %w", err)
				}
				if !resp.OK {
					return fmt.Errorf("daemon error: %s", resp.Error)
				}
				var tasks map[string]config.TaskConfig
				json.Unmarshal(resp.Data, &tasks)
				if len(tasks) == 0 {
					info.Println("No tasks configured.")
					return nil
				}
				for name, task := range tasks {
					status := "disabled"
					if task.Enabled {
						status = "enabled"
					}
					model := cfg.TaskModel(task)
					fmt.Printf("  %-25s %-20s %-8s %s\n", name, task.Schedule, model, status)
				}
				return nil
			}

			if len(cfg.Tasks) == 0 {
				info.Println("No tasks configured.")
				return nil
			}

			for name, task := range cfg.Tasks {
				status := "disabled"
				if task.Enabled {
					status = "enabled"
				}

				model := cfg.TaskModel(task)
				fmt.Printf("  %-25s %-20s %-8s %s\n", name, task.Schedule, model, status)
			}

			return nil
		},
```

- [ ] **Step 4: Add daemon import to task.go**

Add to the imports:

```go
	"encoding/json"

	"github.com/blackpaw-studio/leo/internal/daemon"
```

- [ ] **Step 5: Run all tests**

Run: `go test -race ./...`
Expected: ALL PASS

- [ ] **Step 6: Commit**

```bash
git add internal/cli/task.go
git commit -m "feat: add daemon passthrough to task CLI commands"
```

---

### Task 8: Integrate Server into RunSupervised

**Files:**
- Modify: `internal/service/process.go:142-144` (RunSupervised signature)
- Modify: `internal/service/process.go:146` (defaultSupervisedExec signature)
- Modify: `internal/cli/chat.go:55` (call site)
- Modify: `internal/service/service_test.go` (update test)

- [ ] **Step 1: Update RunSupervised signature**

In `internal/service/process.go`, change lines 139-144:

```go
// RunSupervised runs claude in a restart loop with exponential backoff.
// This is invoked when leo chat --supervised is used. It handles SIGTERM/SIGINT
// for graceful shutdown. The daemon IPC server runs alongside the tmux loop.
func RunSupervised(claudePath string, claudeArgs []string, workDir, configPath string) error {
	return supervisedExecFn(claudePath, claudeArgs, workDir, configPath)
}
```

- [ ] **Step 2: Update defaultSupervisedExec to start daemon server**

Change the signature and add server lifecycle at line 146:

```go
func defaultSupervisedExec(claudePath string, claudeArgs []string, workDir, configPath string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Start daemon IPC server
	sockPath := filepath.Join(workDir, "state", "leo.sock")
	srv := daemon.New(sockPath, configPath)
	if err := srv.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: daemon server failed to start: %v\n", err)
		// Continue without daemon — not fatal
	} else {
		defer srv.Shutdown()
		fmt.Fprintf(os.Stdout, "daemon IPC server listening on %s\n", sockPath)
	}

	backoff := initialBackoff
	// ... rest of function unchanged
```

Add import for `"github.com/blackpaw-studio/leo/internal/daemon"` to the file.

- [ ] **Step 3: Update the testability seam type**

Change the `supervisedExecFn` var declaration at line 27:

```go
supervisedExecFn = defaultSupervisedExec
```

This var's type is inferred. Since the signature changes, update the test that sets it.

- [ ] **Step 4: Update call site in chat.go**

In `internal/cli/chat.go`, line 55, pass the config path:

```go
		cfgPath, err := resolveConfigPath(cfg)
		if err != nil {
			return fmt.Errorf("resolving config path: %w", err)
		}
		return service.RunSupervised(claudePath, claudeArgs, cfg.Agent.Workspace, cfgPath)
```

- [ ] **Step 5: Update service_test.go**

Find the test that sets `supervisedExecFn` and update it to accept the new signature. In `internal/service/service_test.go`, around line 406-418:

```go
	supervisedExecFn = func(claudePath string, claudeArgs []string, workDir, configPath string) error {
		calledPath = claudePath
		calledArgs = claudeArgs
		calledDir = workDir
		return nil
	}

	err := RunSupervised("/usr/bin/claude", []string{"--agent", "test"}, "/workspace", "/workspace/leo.yaml")
```

- [ ] **Step 6: Run all tests**

Run: `go test -race ./...`
Expected: ALL PASS

- [ ] **Step 7: Commit**

```bash
git add internal/service/process.go internal/cli/chat.go internal/service/service_test.go
git commit -m "feat: start daemon IPC server in supervised mode"
```

---

### Task 9: Full Integration Test

**Files:**
- Modify: `e2e/e2e_test.go` (add daemon IPC test)

- [ ] **Step 1: Write integration test**

Add to `e2e/e2e_test.go`:

```go
func TestDaemonIPC(t *testing.T) {
	// Start a daemon server, send requests via the client, verify behavior
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "state", "leo.sock")
	cfgPath := filepath.Join(dir, "leo.yaml")
	os.MkdirAll(filepath.Join(dir, "state"), 0750)

	cfgYAML := fmt.Sprintf(`agent:
  name: test-agent
  workspace: %s
telegram:
  bot_token: "fake-token"
  chat_id: "12345"
defaults:
  model: sonnet
  max_turns: 15
tasks:
  heartbeat:
    schedule: "0 9 * * *"
    prompt_file: HEARTBEAT.md
    enabled: true
`, dir)
	os.WriteFile(cfgPath, []byte(cfgYAML), 0600)
	os.WriteFile(filepath.Join(dir, "HEARTBEAT.md"), []byte("check in"), 0600)

	srv := daemon.New(sockPath, cfgPath)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer srv.Shutdown()

	// Test health
	if !daemon.IsRunning(dir) {
		t.Fatal("daemon should be running")
	}

	// Test task list
	resp, err := daemon.Send(dir, "GET", "/task/list", nil)
	if err != nil {
		t.Fatalf("task list: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task list failed: %s", resp.Error)
	}

	// Test task add
	resp, err = daemon.Send(dir, "POST", "/task/add", daemon.TaskAddRequest{
		Name:       "news",
		Schedule:   "0 7 * * *",
		PromptFile: "NEWS.md",
		Enabled:    true,
	})
	if err != nil {
		t.Fatalf("task add: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task add failed: %s", resp.Error)
	}

	// Verify config updated
	updated, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := updated.Tasks["news"]; !ok {
		t.Error("task 'news' not found after add")
	}

	// Test task disable
	resp, err = daemon.Send(dir, "POST", "/task/disable", daemon.TaskNameRequest{Name: "news"})
	if err != nil {
		t.Fatalf("task disable: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task disable failed: %s", resp.Error)
	}

	updated, err = config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Tasks["news"].Enabled {
		t.Error("task 'news' should be disabled")
	}

	// Test task remove
	resp, err = daemon.Send(dir, "POST", "/task/remove", daemon.TaskNameRequest{Name: "news"})
	if err != nil {
		t.Fatalf("task remove: %v", err)
	}
	if !resp.OK {
		t.Fatalf("task remove failed: %s", resp.Error)
	}

	updated, err = config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := updated.Tasks["news"]; ok {
		t.Error("task 'news' should have been removed")
	}
}
```

Add imports for `daemon` and `config` packages to the e2e test file.

- [ ] **Step 2: Run the e2e test**

Run: `go test -race -tags e2e ./e2e/ -run TestDaemonIPC -v`
Expected: PASS

- [ ] **Step 3: Run full test suite**

Run: `go test -race ./... && go test -race -tags e2e ./e2e/`
Expected: ALL PASS

- [ ] **Step 4: Commit**

```bash
git add e2e/e2e_test.go
git commit -m "test: add daemon IPC integration test"
```

---

### Task 10: Update Documentation

**Files:**
- Modify: `docs/cli/run.md`
- Modify: `CLAUDE.md`

- [ ] **Step 1: Update CLAUDE.md architecture section**

Add `internal/daemon/` to the architecture listing:

```
internal/daemon/          → Daemon IPC server (Unix socket HTTP) + client for CLI passthrough
```

- [ ] **Step 2: Update docs/cli/run.md or create docs/cli/daemon.md**

Add a brief section in the docs explaining the daemon IPC:

- The daemon starts an HTTP server on `{workspace}/state/leo.sock`
- CLI commands auto-detect the daemon and forward requests
- This bypasses Claude Code's sandbox for privileged operations
- Falls back to local execution when no daemon is running

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md docs/
git commit -m "docs: document daemon IPC architecture"
```
