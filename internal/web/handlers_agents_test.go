package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/cron"
)

const testConfigWithTemplatesYAML = `
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
templates:
  coding:
    model: sonnet
    max_turns: 200
    permission_mode: bypassPermissions
  research:
    model: opus
    max_turns: 50
`

// mockAgentService implements AgentService for testing.
type mockAgentService struct {
	spawnCalled bool
	spawnSpec   agent.SpawnSpec
	spawnResult agent.Record
	spawnErr    error

	stopCalled bool
	stopName   string
	stopErr    error

	renameCalled  bool
	renameQuery   string
	renameNewName string
	renameResult  agent.Record
	renameErr     error

	records []agent.Record
}

func (m *mockAgentService) Spawn(_ context.Context, spec agent.SpawnSpec) (agent.Record, error) {
	m.spawnCalled = true
	m.spawnSpec = spec
	if m.spawnErr != nil {
		return agent.Record{}, m.spawnErr
	}
	if m.spawnResult.Name != "" {
		return m.spawnResult, nil
	}
	// Simulate name deduplication for the dedup test
	name := fmt.Sprintf("leo-%s-%s", spec.Template, spec.Repo)
	for _, r := range m.records {
		if r.Name == name {
			name += "-2"
			break
		}
	}
	return agent.Record{Name: name, Template: spec.Template, Status: "starting"}, nil
}

func (m *mockAgentService) Stop(name string) error {
	m.stopCalled = true
	m.stopName = name
	return m.stopErr
}

func (m *mockAgentService) List() []agent.Record {
	return m.records
}

func (m *mockAgentService) Rename(query, newName string) (agent.Record, error) {
	m.renameCalled = true
	m.renameQuery = query
	m.renameNewName = newName
	if m.renameErr != nil {
		return agent.Record{}, m.renameErr
	}
	if m.renameResult.Name != "" {
		return m.renameResult, nil
	}
	return agent.Record{Name: "leo-" + newName, Status: "running"}, nil
}

// Resolve does exact-name matching against the fake's records so tests that
// drive the shorthand-aware web handlers can stick to canonical names. The
// full Manager.Resolve algorithm is covered by internal/agent/resolve_test.go.
func (m *mockAgentService) Resolve(query string) (agent.Record, error) {
	for _, r := range m.records {
		if r.Name == query {
			return r, nil
		}
	}
	return agent.Record{}, &agent.ErrNotFound{Query: query}
}

func newTestServerWithAgents(t *testing.T) (*Server, string, *mockAgentService) {
	t.Helper()
	dir, err := os.MkdirTemp("", "leo-web-agent-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(testConfigWithTemplatesYAML), 0600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	os.MkdirAll(filepath.Join(dir, "state"), 0750)

	processes := &mockProcesses{
		states: map[string]ProcessStateInfo{
			"assistant": {Name: "assistant", Status: "running", StartedAt: time.Now()},
		},
	}
	scheduler := &mockScheduler{entries: []cron.EntryInfo{}}
	reloader := &mockReloader{}
	svc := &mockAgentService{
		records: []agent.Record{
			{Name: "leo-coding-leo", Status: "running", StartedAt: time.Now()},
		},
	}

	s := New(cfgPath, processes, scheduler, reloader, svc, Options{Port: testPort, APIToken: testAPIToken})

	// Auto-authorize requests so existing test assertions survive the
	// addition of Host/Origin and bearer middleware. Callers that need to
	// exercise the raw middleware can build their own Server.
	rawHandler := s.httpServer.Handler
	s.httpServer.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizeTestRequest(r)
		rawHandler.ServeHTTP(w, r)
	})
	return s, dir, svc
}

// --- API Tests ---

func TestAPITemplateList(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	req := httptest.NewRequest("GET", "/api/template/list", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp apiResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected ok=true, got error: %s", resp.Error)
	}

	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("expected map data, got %T", resp.Data)
	}
	if len(data) != 2 {
		t.Errorf("expected 2 templates, got %d", len(data))
	}
}

func TestAPIAgentList(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	req := httptest.NewRequest("GET", "/api/agent/list", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp apiResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !resp.OK {
		t.Fatal("expected ok=true")
	}

	data, ok := resp.Data.([]any)
	if !ok {
		t.Fatalf("expected array data, got %T", resp.Data)
	}
	if len(data) == 0 {
		t.Error("expected at least one agent in list")
	}
}

func TestAPIAgentListNoService(t *testing.T) {
	dir, _ := os.MkdirTemp("", "leo-web-test-*")
	defer os.RemoveAll(dir)
	cfgPath := filepath.Join(dir, "leo.yaml")
	os.WriteFile(cfgPath, []byte(testConfigWithTemplatesYAML), 0600)
	os.MkdirAll(filepath.Join(dir, "state"), 0750)

	s := New(cfgPath, nil, nil, nil, nil, Options{Port: testPort, APIToken: testAPIToken})

	req := httptest.NewRequest("GET", "/api/agent/list", nil)
	req.Host = testHost
	req.Header.Set("Authorization", "Bearer "+testAPIToken)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 even with nil service, got %d", w.Code)
	}
}

func TestAPIAgentSpawn(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)

	body := `{"template":"coding","repo":"test-project"}`
	req := httptest.NewRequest("POST", "/api/agent/spawn", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if !svc.spawnCalled {
		t.Fatal("expected Spawn to be called")
	}
	if svc.spawnSpec.Template != "coding" {
		t.Errorf("expected template=coding, got %q", svc.spawnSpec.Template)
	}
	if svc.spawnSpec.Repo != "test-project" {
		t.Errorf("expected repo=test-project, got %q", svc.spawnSpec.Repo)
	}
}

func TestAPIAgentSpawnNoService(t *testing.T) {
	dir, _ := os.MkdirTemp("", "leo-web-test-*")
	defer os.RemoveAll(dir)
	cfgPath := filepath.Join(dir, "leo.yaml")
	os.WriteFile(cfgPath, []byte(testConfigWithTemplatesYAML), 0600)
	os.MkdirAll(filepath.Join(dir, "state"), 0750)

	s := New(cfgPath, nil, nil, nil, nil, Options{Port: testPort, APIToken: testAPIToken})

	body := `{"template":"coding","repo":"test"}`
	req := httptest.NewRequest("POST", "/api/agent/spawn", strings.NewReader(body))
	req.Host = testHost
	req.Header.Set("Authorization", "Bearer "+testAPIToken)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestAPIAgentStop(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)

	body := `{"name":"leo-coding-leo"}`
	req := httptest.NewRequest("POST", "/api/agent/stop", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if !svc.stopCalled {
		t.Fatal("expected Stop to be called")
	}
	if svc.stopName != "leo-coding-leo" {
		t.Errorf("expected stop name 'leo-coding-leo', got %q", svc.stopName)
	}
}

func TestAPIAgentStopMissingName(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	body := `{}`
	req := httptest.NewRequest("POST", "/api/agent/stop", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestAPIAgentRename(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	svc.renameResult = agent.Record{Name: "leo-renamed", Status: "running"}

	body := `{"new_name":"renamed"}`
	req := httptest.NewRequest("POST", "/api/agent/leo-coding-leo/rename", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !svc.renameCalled {
		t.Fatal("expected Rename to be called")
	}
	if svc.renameQuery != "leo-coding-leo" {
		t.Errorf("expected rename query 'leo-coding-leo', got %q", svc.renameQuery)
	}
	if svc.renameNewName != "renamed" {
		t.Errorf("expected new name 'renamed', got %q", svc.renameNewName)
	}

	var resp apiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !resp.OK {
		t.Errorf("expected ok=true, got %+v", resp)
	}
	data, ok := resp.Data.(map[string]any)
	if !ok || data["name"] != "leo-renamed" {
		t.Errorf("expected data.name 'leo-renamed', got %v", resp.Data)
	}
}

func TestAPIAgentRenameMissingNewName(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)

	body := `{}`
	req := httptest.NewRequest("POST", "/api/agent/leo-coding-leo/rename", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
	if svc.renameCalled {
		t.Error("expected Rename not to be called when new_name is missing")
	}
}

func TestAPIAgentRenameCollision(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	svc.renameErr = agent.ErrAgentNameTaken

	body := `{"new_name":"taken"}`
	req := httptest.NewRequest("POST", "/api/agent/leo-coding-leo/rename", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("expected 409 for name collision, got %d", w.Code)
	}
}
