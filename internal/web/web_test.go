package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/cron"
)

const testConfigYAML = `
defaults:
  model: sonnet
  max_turns: 10
web:
  enabled: true
  port: 8370
processes:
  assistant:
    workspace: /tmp/test
    enabled: true
tasks:
  heartbeat:
    schedule: "0 * * * *"
    prompt_file: heartbeat.md
    enabled: true
  cleanup:
    schedule: "0 3 * * 0"
    prompt_file: cleanup.md
    enabled: false
`

func writeTestConfig(t *testing.T, dir string) string {
	t.Helper()
	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(testConfigYAML), 0600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	// Create state dir for history
	os.MkdirAll(filepath.Join(dir, "state"), 0750)
	return cfgPath
}

type mockProcesses struct {
	states map[string]ProcessStateInfo
}

func (m *mockProcesses) States() map[string]ProcessStateInfo {
	return m.states
}

type mockScheduler struct {
	entries []cron.EntryInfo
}

func (m *mockScheduler) List() []cron.EntryInfo {
	return m.entries
}

type mockReloader struct {
	called bool
	err    error
}

func (m *mockReloader) ReloadConfig() error {
	m.called = true
	return m.err
}

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "leo-web-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)

	processes := &mockProcesses{
		states: map[string]ProcessStateInfo{
			"assistant": {
				Name:      "assistant",
				Status:    "running",
				StartedAt: time.Now().Add(-2 * time.Hour),
				Restarts:  0,
			},
		},
	}

	scheduler := &mockScheduler{
		entries: []cron.EntryInfo{
			{Name: "heartbeat", Schedule: "0 * * * *", Next: time.Now().Add(30 * time.Minute)},
		},
	}

	reloader := &mockReloader{}

	s := New(cfgPath, processes, scheduler, reloader, nil)
	return s, dir
}

func TestDashboardReturns200(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("expected text/html content type, got %q", ct)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Leo") {
		t.Error("expected page to contain 'Leo'")
	}
	if !strings.Contains(body, "assistant") {
		t.Error("expected page to contain process name 'assistant'")
	}
	if !strings.Contains(body, "heartbeat") {
		t.Error("expected page to contain task name 'heartbeat'")
	}
}

func TestStaticServing(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/static/style.css", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "--bg-page") {
		t.Error("expected CSS to contain --bg-page variable")
	}
}

func TestPartialStatusReturnsFragment(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/partials/status", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	// Should be a fragment, not a full HTML document
	if strings.Contains(body, "<!DOCTYPE") {
		t.Error("partial should not be a full HTML document")
	}
	if !strings.Contains(body, "status-banner") {
		t.Error("expected status banner fragment")
	}
}

func TestPartialProcessesReturnsFragment(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/partials/processes", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "assistant") {
		t.Error("expected process card for 'assistant'")
	}
}

func TestPartialTasksReturnsFragment(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/partials/tasks", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "heartbeat") {
		t.Error("expected task 'heartbeat'")
	}
	if !strings.Contains(body, "cleanup") {
		t.Error("expected task 'cleanup'")
	}
}

func TestPartialConfigSettingsReturnsFragment(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/partials/config/settings", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "sonnet") {
		t.Error("expected settings to show model 'sonnet'")
	}
}

func TestPartialConfigProcessesReturnsFragment(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/partials/config/processes", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "assistant") {
		t.Error("expected processes tab to show 'assistant'")
	}
	if !strings.Contains(body, "Add Process") {
		t.Error("expected processes tab to show add form")
	}
}

func TestPartialConfigTasksReturnsFragment(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/partials/config/tasks", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "heartbeat") {
		t.Error("expected tasks tab to show 'heartbeat'")
	}
	if !strings.Contains(body, "Add Task") {
		t.Error("expected tasks tab to show add form")
	}
}

func TestTaskToggle(t *testing.T) {
	s, dir := newTestServer(t)

	req := httptest.NewRequest("POST", "/web/task/heartbeat/toggle", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "disabled") {
		t.Error("expected flash to say 'disabled'")
	}

	// Verify config was updated
	data, err := os.ReadFile(filepath.Join(dir, "leo.yaml"))
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	// After toggling, the config (YAML) should reflect the change
	if !strings.Contains(string(data), "heartbeat") {
		t.Error("expected config to still contain heartbeat task")
	}
}

func TestTaskToggleNotFound(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest("POST", "/web/task/nonexistent/toggle", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 (flash response), got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "not found") {
		t.Error("expected error flash for not found task")
	}
}

func TestConfigReload(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest("POST", "/web/config/reload", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "reloaded") {
		t.Error("expected success flash for config reload")
	}
}

func TestDashboard404ForOtherPaths(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/nonexistent", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

func TestDashboardEmptyState(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-web-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgYAML := `
defaults:
  model: sonnet
`
	cfgPath := filepath.Join(dir, "leo.yaml")
	os.WriteFile(cfgPath, []byte(cfgYAML), 0600)
	os.MkdirAll(filepath.Join(dir, "state"), 0750)

	s := New(cfgPath, nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "No processes configured") {
		t.Error("expected empty state message for processes")
	}
	if !strings.Contains(body, "No tasks configured") {
		t.Error("expected empty state message for tasks")
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{5 * time.Minute, "5m"},
		{2*time.Hour + 30*time.Minute, "2h 30m"},
		{3 * time.Hour, "3h"},
		{26 * time.Hour, "1d 2h"},
		{48 * time.Hour, "2d"},
	}

	for _, tt := range tests {
		got := formatDuration(tt.d)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestDescribeCron(t *testing.T) {
	tests := []struct {
		expr string
		want string
	}{
		{"* * * * *", "Every minute"},
		{"*/15 * * * *", "Every 15 minutes"},
		{"0 */2 * * *", "Every 2 hours"},
		{"0 9 * * *", "Daily at 9:00 AM"},
		{"30 10 * * 1-5", "Weekdays at 10:30 AM"},
		{"0 7 * * *", "Daily at 7:00 AM"},
		{"0,30 7-21 * * *", "Daily at :00 and :30, 7 AM–9 PM"},
		{"15 10 * * 1-5", "Weekdays at 10:15 AM"},
		{"0 3 * * 0", "Sun at 3:00 AM"},
		{"0 0 * * *", "Daily at 12:00 AM"},
		{"0 12 * * *", "Daily at 12:00 PM"},
		{"0 17 * * *", "Daily at 5:00 PM"},
	}

	for _, tt := range tests {
		got := describeCron(tt.expr)
		if got != tt.want {
			t.Errorf("describeCron(%q) = %q, want %q", tt.expr, got, tt.want)
		}
	}
}

func TestStatusColor(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"running", "status-running"},
		{"restarting", "status-restarting"},
		{"stopped", "status-stopped"},
		{"disabled", "status-disabled"},
		{"unknown", "status-disabled"},
	}

	for _, tt := range tests {
		got := statusColor(tt.status)
		if got != tt.want {
			t.Errorf("statusColor(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestParseLogEvents(t *testing.T) {
	t.Run("stream-json NDJSON", func(t *testing.T) {
		input := `{"type":"system","subtype":"init","session_id":"abc-123"}
{"type":"assistant","message":{"content":[{"type":"text","text":"Let me check."}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"/tmp/test"}}]}}
{"type":"tool_result","content":"file contents here"}
{"type":"result","session_id":"abc-123","result":"Done","cost_usd":0.05,"num_turns":2}
`
		events := parseLogEvents([]byte(input))

		if len(events) != 5 {
			t.Fatalf("got %d events, want 5", len(events))
		}

		if events[0].Type != "system" {
			t.Errorf("event[0].Type = %q, want system", events[0].Type)
		}
		if events[1].Type != "assistant" || events[1].Content != "Let me check." {
			t.Errorf("event[1] = %+v, want assistant with text", events[1])
		}
		if events[2].Type != "tool_use" || events[2].Tool != "Read" {
			t.Errorf("event[2] = %+v, want tool_use Read", events[2])
		}
		if events[3].Type != "tool_result" || events[3].Content != "file contents here" {
			t.Errorf("event[3] = %+v, want tool_result", events[3])
		}
		if events[4].Type != "result" || events[4].Content != "Done" || events[4].Cost != "$0.0500" {
			t.Errorf("event[4] = %+v, want result with cost", events[4])
		}
	})

	t.Run("plain text fallback", func(t *testing.T) {
		input := "just plain text output\nno JSON here"
		events := parseLogEvents([]byte(input))

		if len(events) != 1 {
			t.Fatalf("got %d events, want 1", len(events))
		}
		if events[0].Type != "raw" {
			t.Errorf("event[0].Type = %q, want raw", events[0].Type)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		events := parseLogEvents([]byte(""))
		if len(events) != 0 {
			t.Errorf("got %d events for empty input, want 0", len(events))
		}
	})
}

func TestTaskPromptGet_FileExists(t *testing.T) {
	s, dir := newTestServer(t)

	// Create workspace and prompt file
	ws := filepath.Join(dir, "workspace")
	os.MkdirAll(ws, 0750)
	os.WriteFile(filepath.Join(ws, "heartbeat.md"), []byte("# Heartbeat\nCheck systems"), 0644)

	req := httptest.NewRequest("GET", "/web/task/heartbeat/prompt", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Check systems") {
		t.Error("response should contain prompt file content")
	}
	if !strings.Contains(body, "prompt_content") {
		t.Error("response should contain textarea with name prompt_content")
	}
}

func TestTaskPromptGet_FileNotFound(t *testing.T) {
	s, dir := newTestServer(t)

	// Create workspace but not the prompt file
	ws := filepath.Join(dir, "workspace")
	os.MkdirAll(ws, 0750)

	req := httptest.NewRequest("GET", "/web/task/heartbeat/prompt", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "prompt_content") {
		t.Error("response should contain textarea even for missing file")
	}
}

func TestTaskPromptGet_TaskNotFound(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest("GET", "/web/task/nonexistent/prompt", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "not found") {
		t.Error("response should contain 'not found' error")
	}
}

func TestTaskPromptGet_PathTraversal(t *testing.T) {
	s, dir := newTestServer(t)

	// Overwrite config with a task that has a traversal path
	cfgYAML := `
tasks:
  evil:
    schedule: "0 * * * *"
    prompt_file: "../../etc/passwd"
    enabled: true
`
	os.WriteFile(filepath.Join(dir, "leo.yaml"), []byte(cfgYAML), 0600)

	req := httptest.NewRequest("GET", "/web/task/evil/prompt", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "escapes workspace") {
		t.Errorf("response should contain 'escapes workspace' error, got: %s", body)
	}
}

func TestTaskPromptSave_Success(t *testing.T) {
	s, dir := newTestServer(t)

	// Create workspace
	ws := filepath.Join(dir, "workspace")
	os.MkdirAll(ws, 0750)
	os.WriteFile(filepath.Join(ws, "heartbeat.md"), []byte("old content"), 0644)

	form := strings.NewReader("prompt_content=new+content+here")
	req := httptest.NewRequest("POST", "/web/task/heartbeat/prompt", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "saved") {
		t.Errorf("response should contain 'saved', got: %s", body)
	}

	// Verify file was written
	data, err := os.ReadFile(filepath.Join(ws, "heartbeat.md"))
	if err != nil {
		t.Fatalf("reading written file: %v", err)
	}
	if string(data) != "new content here" {
		t.Errorf("file content = %q, want %q", string(data), "new content here")
	}
}

func TestTaskPromptSave_CreatesFile(t *testing.T) {
	s, dir := newTestServer(t)

	// Workspace doesn't exist yet — WritePromptFile should create it
	form := strings.NewReader("prompt_content=brand+new+prompt")
	req := httptest.NewRequest("POST", "/web/task/heartbeat/prompt", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "saved") {
		t.Errorf("response should contain 'saved', got: %s", body)
	}

	// Verify file + directories were created
	ws := filepath.Join(dir, "workspace")
	data, err := os.ReadFile(filepath.Join(ws, "heartbeat.md"))
	if err != nil {
		t.Fatalf("reading created file: %v", err)
	}
	if string(data) != "brand new prompt" {
		t.Errorf("file content = %q, want %q", string(data), "brand new prompt")
	}
}

func TestTaskPromptSave_PathTraversal(t *testing.T) {
	s, dir := newTestServer(t)

	cfgYAML := `
tasks:
  evil:
    schedule: "0 * * * *"
    prompt_file: "../../etc/evil.md"
    enabled: true
`
	os.WriteFile(filepath.Join(dir, "leo.yaml"), []byte(cfgYAML), 0600)

	form := strings.NewReader("prompt_content=malicious")
	req := httptest.NewRequest("POST", "/web/task/evil/prompt", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "escapes workspace") {
		t.Errorf("response should contain 'escapes workspace' error, got: %s", body)
	}
}

func TestTaskPromptSave_TaskNotFound(t *testing.T) {
	s, _ := newTestServer(t)

	form := strings.NewReader("prompt_content=test")
	req := httptest.NewRequest("POST", "/web/task/nonexistent/prompt", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "not found") {
		t.Error("response should contain 'not found' error")
	}
}
