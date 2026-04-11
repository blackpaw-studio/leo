package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
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

// mockAgentManager implements AgentManager for testing.
type mockAgentManager struct {
	spawnCalled bool
	spawnSpec   AgentSpawnRequest
	spawnErr    error

	stopCalled bool
	stopName   string
	stopErr    error

	agents map[string]ProcessStateInfo
}

func (m *mockAgentManager) SpawnAgent(spec AgentSpawnRequest) error {
	m.spawnCalled = true
	m.spawnSpec = spec
	return m.spawnErr
}

func (m *mockAgentManager) StopAgent(name string) error {
	m.stopCalled = true
	m.stopName = name
	return m.stopErr
}

func (m *mockAgentManager) EphemeralAgents() map[string]ProcessStateInfo {
	if m.agents == nil {
		return map[string]ProcessStateInfo{}
	}
	return m.agents
}

func newTestServerWithAgents(t *testing.T) (*Server, string, *mockAgentManager) {
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
	agentMgr := &mockAgentManager{
		agents: map[string]ProcessStateInfo{
			"leo-coding-leo": {
				Name: "leo-coding-leo", Status: "running",
				StartedAt: time.Now(), Ephemeral: true,
			},
		},
	}

	s := New(cfgPath, processes, scheduler, reloader, agentMgr)
	return s, dir, agentMgr
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

	// Data should contain our 2 templates
	data, ok := resp.Data.(map[string]interface{})
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

	data, ok := resp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map data, got %T", resp.Data)
	}
	if _, exists := data["leo-coding-leo"]; !exists {
		t.Error("expected leo-coding-leo in list")
	}
}

func TestAPIAgentListNoManager(t *testing.T) {
	dir, _ := os.MkdirTemp("", "leo-web-test-*")
	defer os.RemoveAll(dir)
	cfgPath := filepath.Join(dir, "leo.yaml")
	os.WriteFile(cfgPath, []byte(testConfigWithTemplatesYAML), 0600)
	os.MkdirAll(filepath.Join(dir, "state"), 0750)

	s := New(cfgPath, nil, nil, nil, nil)

	req := httptest.NewRequest("GET", "/api/agent/list", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 even with nil manager, got %d", w.Code)
	}
}

func TestAPIAgentSpawn(t *testing.T) {
	s, _, mgr := newTestServerWithAgents(t)

	body := `{"template":"coding","repo":"test-project"}`
	req := httptest.NewRequest("POST", "/api/agent/spawn", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if !mgr.spawnCalled {
		t.Fatal("expected SpawnAgent to be called")
	}
	if !strings.Contains(mgr.spawnSpec.Name, "leo-coding-test-project") {
		t.Errorf("expected agent name containing 'leo-coding-test-project', got %q", mgr.spawnSpec.Name)
	}
}

func TestAPIAgentSpawnMissingFields(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	body := `{"template":"coding"}`
	req := httptest.NewRequest("POST", "/api/agent/spawn", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestAPIAgentSpawnInvalidTemplate(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	body := `{"template":"nonexistent","repo":"test"}`
	req := httptest.NewRequest("POST", "/api/agent/spawn", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestAPIAgentSpawnNoManager(t *testing.T) {
	dir, _ := os.MkdirTemp("", "leo-web-test-*")
	defer os.RemoveAll(dir)
	cfgPath := filepath.Join(dir, "leo.yaml")
	os.WriteFile(cfgPath, []byte(testConfigWithTemplatesYAML), 0600)
	os.MkdirAll(filepath.Join(dir, "state"), 0750)

	s := New(cfgPath, nil, nil, nil, nil)

	body := `{"template":"coding","repo":"test"}`
	req := httptest.NewRequest("POST", "/api/agent/spawn", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", w.Code)
	}
}

func TestAPIAgentStop(t *testing.T) {
	s, _, mgr := newTestServerWithAgents(t)

	body := `{"name":"leo-coding-leo"}`
	req := httptest.NewRequest("POST", "/api/agent/stop", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if !mgr.stopCalled {
		t.Fatal("expected StopAgent to be called")
	}
	if mgr.stopName != "leo-coding-leo" {
		t.Errorf("expected stop name 'leo-coding-leo', got %q", mgr.stopName)
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

func TestAPIAgentSpawnNameDeduplication(t *testing.T) {
	s, _, mgr := newTestServerWithAgents(t)
	// leo-coding-leo already exists in mockAgentManager.agents

	body := `{"template":"coding","repo":"leo"}`
	req := httptest.NewRequest("POST", "/api/agent/spawn", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Name should have been deduplicated (suffix -2)
	if mgr.spawnSpec.Name != "leo-coding-leo-2" {
		t.Errorf("expected deduplicated name 'leo-coding-leo-2', got %q", mgr.spawnSpec.Name)
	}
}

// --- resolveAgentWorkspace Tests ---

func TestResolveAgentWorkspacePlainName(t *testing.T) {
	dir := t.TempDir()
	tmpl := config.TemplateConfig{Workspace: dir}

	workspace, name, err := resolveAgentWorkspace(tmpl, "coding", "myproject", "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if workspace != dir {
		t.Errorf("workspace = %q, want %q", workspace, dir)
	}
	if name != "leo-coding-myproject" {
		t.Errorf("name = %q, want leo-coding-myproject", name)
	}
}

func TestResolveAgentWorkspaceWithSlashExistingClone(t *testing.T) {
	dir := t.TempDir()
	// Pre-create the repo directory with .git to simulate existing clone
	repoDir := filepath.Join(dir, "myrepo", ".git")
	os.MkdirAll(repoDir, 0750)

	tmpl := config.TemplateConfig{Workspace: dir}

	workspace, name, err := resolveAgentWorkspace(tmpl, "coding", "owner/myrepo", "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	expected := filepath.Join(dir, "myrepo")
	if workspace != expected {
		t.Errorf("workspace = %q, want %q", workspace, expected)
	}
	if name != "leo-coding-owner-myrepo" {
		t.Errorf("name = %q, want leo-coding-owner-myrepo", name)
	}
}

func TestResolveAgentWorkspaceNameOverride(t *testing.T) {
	dir := t.TempDir()
	tmpl := config.TemplateConfig{Workspace: dir}

	_, name, err := resolveAgentWorkspace(tmpl, "coding", "test", "custom-name")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if name != "custom-name" {
		t.Errorf("name = %q, want custom-name", name)
	}
}

func TestResolveAgentWorkspaceDefaultWorkspace(t *testing.T) {
	tmpl := config.TemplateConfig{} // no workspace set

	workspace, _, err := resolveAgentWorkspace(tmpl, "coding", "test", "")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	// Should fall back to ~/.leo/agents
	if workspace == "" {
		t.Error("expected non-empty default workspace")
	}
}

// --- buildTemplateArgs Tests ---

func TestBuildTemplateArgsBasic(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10},
	}
	tmpl := config.TemplateConfig{
		Model:    "opus",
		MaxTurns: 200,
	}

	args := buildTemplateArgs(cfg, tmpl, "test-agent", "/tmp/workspace")

	assertContainsFlag(t, args, "--model", "opus")
	assertContainsFlag(t, args, "--max-turns", "200")
	assertContainsFlag(t, args, "--add-dir", "/tmp/workspace")
	// Remote control should default to on
	assertContains(t, args, "--remote-control")
	assertContainsFlag(t, args, "--remote-control-session-name-prefix", "test-agent")
}

func TestBuildTemplateArgsInheritsDefaults(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{
			Model:              "haiku",
			MaxTurns:           50,
			PermissionMode:     "auto",
			AllowedTools:       []string{"Read", "Write"},
			AppendSystemPrompt: "be helpful",
		},
	}
	tmpl := config.TemplateConfig{} // all empty — should inherit

	args := buildTemplateArgs(cfg, tmpl, "test", "/tmp/ws")

	assertContainsFlag(t, args, "--model", "haiku")
	assertContainsFlag(t, args, "--max-turns", "50")
	assertContainsFlag(t, args, "--permission-mode", "auto")
	assertContainsFlag(t, args, "--allowed-tools", "Read,Write")
	assertContainsFlag(t, args, "--append-system-prompt", "be helpful")
}

func TestBuildTemplateArgsChannels(t *testing.T) {
	cfg := &config.Config{}
	tmpl := config.TemplateConfig{
		Channels: []string{"plugin:telegram@official", "plugin:slack@custom"},
	}

	args := buildTemplateArgs(cfg, tmpl, "test", "/tmp/ws")

	count := 0
	for _, a := range args {
		if a == "--channels" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("expected 2 --channels flags, got %d", count)
	}
}

func TestBuildTemplateArgsAgent(t *testing.T) {
	cfg := &config.Config{}
	tmpl := config.TemplateConfig{Agent: "my-agent"}

	args := buildTemplateArgs(cfg, tmpl, "test", "/tmp/ws")
	assertContainsFlag(t, args, "--agent", "my-agent")
}

func TestBuildTemplateArgsRemoteControlDisabled(t *testing.T) {
	cfg := &config.Config{}
	rc := false
	tmpl := config.TemplateConfig{RemoteControl: &rc}

	args := buildTemplateArgs(cfg, tmpl, "test", "/tmp/ws")
	for _, a := range args {
		if a == "--remote-control" {
			t.Error("--remote-control should not be present when disabled")
		}
	}
}

// --- Helpers ---

func assertContains(t *testing.T, args []string, flag string) {
	t.Helper()
	for _, a := range args {
		if a == flag {
			return
		}
	}
	t.Errorf("expected args to contain %q, got %v", flag, args)
}

func assertContainsFlag(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] == value {
			return
		}
	}
	t.Errorf("expected args to contain %s %s, got %v", flag, value, args)
}
