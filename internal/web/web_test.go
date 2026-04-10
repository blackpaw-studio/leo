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
