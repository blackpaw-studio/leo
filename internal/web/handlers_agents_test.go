package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/harness"
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
    harness_options:
      permission_mode: bypassPermissions
    env:
      OP_SERVICE_ACCOUNT_TOKEN: ` + testSecretEnvValue + `
      ANTHROPIC_BASE_URL: http://localhost:3325
  research:
    model: opus
    max_turns: 50
`

// testSecretEnvValue is an obviously-fake credential planted in the template
// fixture so leak guards can assert it never reaches an API payload.
const testSecretEnvValue = "ops_totally_fake_token_do_not_use"

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

	suspendCalled bool
	suspendName   string
	suspendErr    error

	resumeCalled bool
	resumeName   string
	resumeResult agent.Record
	resumeErr    error

	restartAllCalled bool
	restartAllResult agent.RestartResult

	records []agent.Record

	// handles backs ResolveHandle for tests exercising non-claude message
	// dispatch: keyed by agent name, maps to (harnessName, SessionHandle).
	// A missing key means ok=false — the caller falls back to tmux/claude.
	handles map[string]resolvedHandle
}

// resolvedHandle is the mockAgentService.handles value type.
type resolvedHandle struct {
	harnessName string
	handle      harness.SessionHandle
}

func (m *mockAgentService) ResolveHandle(name string) (string, harness.SessionHandle, bool) {
	rh, ok := m.handles[name]
	if !ok {
		return "", harness.SessionHandle{}, false
	}
	return rh.harnessName, rh.handle, true
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

func (m *mockAgentService) Suspend(name string) error {
	m.suspendCalled = true
	m.suspendName = name
	return m.suspendErr
}

func (m *mockAgentService) Resume(name string) (agent.Record, error) {
	m.resumeCalled = true
	m.resumeName = name
	if m.resumeErr != nil {
		return agent.Record{}, m.resumeErr
	}
	if m.resumeResult.Name != "" {
		return m.resumeResult, nil
	}
	return agent.Record{Name: name, Status: "starting"}, nil
}

func (m *mockAgentService) RestartAll() agent.RestartResult {
	m.restartAllCalled = true
	return m.restartAllResult
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

	data, ok := resp.Data.([]any)
	if !ok {
		t.Fatalf("expected array data, got %T", resp.Data)
	}
	if len(data) != 2 {
		t.Fatalf("expected 2 templates, got %d", len(data))
	}

	// Sorted by name: coding, research.
	first, ok := data[0].(map[string]any)
	if !ok {
		t.Fatalf("expected object entries, got %T", data[0])
	}
	if first["name"] != "coding" {
		t.Errorf("first template name = %v, want coding (sorted)", first["name"])
	}
	if first["model"] != "sonnet" {
		t.Errorf("first template model = %v, want sonnet", first["model"])
	}

	// Env keys are useful for debugging; env values are credentials.
	keys, ok := first["env_keys"].([]any)
	if !ok {
		t.Fatalf("expected env_keys array, got %T", first["env_keys"])
	}
	want := []any{"ANTHROPIC_BASE_URL", "OP_SERVICE_ACCOUNT_TOKEN"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("env_keys = %v, want %v", keys, want)
	}
	if _, leaked := first["env"]; leaked {
		t.Error("template payload carries an env map; it must expose keys only")
	}
}

// TestAPITemplateListOmitsEnvValues is the leak guard for the leo_list_templates
// MCP tool: /api/template/list is what it serves, so any credential in a
// template's env would land in the calling agent's context verbatim.
func TestAPITemplateListOmitsEnvValues(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	req := httptest.NewRequest("GET", "/api/template/list", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if body := w.Body.String(); strings.Contains(body, testSecretEnvValue) {
		t.Errorf("template list leaked a secret env value; body: %s", body)
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

func TestAPIAgentSpawnWithoutRepoSucceeds(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)

	body := `{"template":"coding"}`
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
	if svc.spawnSpec.Repo != "" {
		t.Errorf("expected empty repo, got %q", svc.spawnSpec.Repo)
	}
}

func TestWebAgentSpawnWithoutRepoSucceeds(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)

	form := url.Values{"template": {"coding"}}
	req := httptest.NewRequest("POST", "/web/agent/spawn", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !svc.spawnCalled {
		t.Fatal("expected Spawn to be called")
	}
	if svc.spawnSpec.Repo != "" {
		t.Errorf("expected empty repo, got %q", svc.spawnSpec.Repo)
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

func TestAPIAgentSuspend(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)

	body := `{"name":"leo-coding-leo"}`
	req := httptest.NewRequest("POST", "/api/agent/suspend", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !svc.suspendCalled {
		t.Fatal("expected Suspend to be called")
	}
	if svc.suspendName != "leo-coding-leo" {
		t.Errorf("expected suspend name 'leo-coding-leo', got %q", svc.suspendName)
	}
}

func TestAPIAgentSuspendMissingName(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	req := httptest.NewRequest("POST", "/api/agent/suspend", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// TestAPIAgentResume passes a name that is NOT among the live records to prove
// the handler forwards it to Resume verbatim rather than round-tripping through
// resolveAgentQuery (which only matches live agents and would 404 a suspended
// one).
func TestAPIAgentResume(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)

	body := `{"name":"suspended-worker"}`
	req := httptest.NewRequest("POST", "/api/agent/resume", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if !svc.resumeCalled {
		t.Fatal("expected Resume to be called")
	}
	if svc.resumeName != "suspended-worker" {
		t.Errorf("expected resume name 'suspended-worker' passed verbatim, got %q", svc.resumeName)
	}
}

func TestAPIAgentResumeMissingName(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	req := httptest.NewRequest("POST", "/api/agent/resume", strings.NewReader(`{}`))
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

// --- Web form rename tests ---

// TestWebAgentRenameError drives the FORM handler (not the API one) with a
// failing Rename and asserts the error flash is retargeted to the shared
// #flash-container rather than outerHTML-swapping (and destroying) the agents
// tab. The retarget headers are the invariant that proves the agents tab
// survives the error.
func TestWebAgentRenameError(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	svc.renameErr = agent.ErrAgentNameTaken

	form := url.Values{"new_name": {"taken"}}
	req := httptest.NewRequest("POST", "/web/agent/leo-coding-leo/rename", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if !svc.renameCalled {
		t.Fatal("expected Rename to be called")
	}

	// The error flash must NOT outerHTML-swap #agents-content. The handler
	// redirects the swap to the shared flash container via htmx headers.
	if got := w.Header().Get("HX-Retarget"); got != "#flash-container" {
		t.Errorf("expected HX-Retarget '#flash-container', got %q", got)
	}
	if got := w.Header().Get("HX-Reswap"); got != "innerHTML" {
		t.Errorf("expected HX-Reswap 'innerHTML', got %q", got)
	}

	// Stock htmx only swaps 2xx responses; the flash conveys the failure.
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 so htmx swaps the flash body, got %d", w.Code)
	}

	// The user must see why it failed, and the agents tab must not be
	// re-rendered (no #agents-content / spawn form in the body).
	body := w.Body.String()
	if !strings.Contains(body, agent.ErrAgentNameTaken.Error()) {
		t.Errorf("expected error message in flash body, got %q", body)
	}
	if !strings.Contains(body, "flash-error") {
		t.Errorf("expected flash markup in body, got %q", body)
	}
	if strings.Contains(body, `id="agents-content"`) {
		t.Errorf("error response must not re-render the agents tab, got %q", body)
	}
}

// TestWebAgentRenameSuccess drives the FORM handler with a valid new name and
// asserts the agents partial is re-rendered in place (so the new name shows),
// and that the success response does NOT retarget the flash container.
func TestWebAgentRenameSuccess(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	svc.renameResult = agent.Record{Name: "leo-renamed", Status: "running"}
	svc.records = []agent.Record{{Name: "leo-renamed", Status: "running", StartedAt: time.Now()}}

	form := url.Values{"new_name": {"renamed"}}
	req := httptest.NewRequest("POST", "/web/agent/leo-coding-leo/rename", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if !svc.renameCalled {
		t.Fatal("expected Rename to be called")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Success re-renders the agents partial in place — no retarget.
	if got := w.Header().Get("HX-Retarget"); got != "" {
		t.Errorf("success path must not set HX-Retarget, got %q", got)
	}

	body := w.Body.String()
	if !strings.Contains(body, `id="agents-content"`) {
		t.Errorf("expected agents partial re-render, got %q", body)
	}
	if !strings.Contains(body, "leo-renamed") {
		t.Errorf("expected renamed agent in re-rendered list, got %q", body)
	}
}

// --- Web form suspend/resume tests ---

// TestWebAgentSuspendSuccess drives the FORM handler with a running agent and
// asserts Suspend is called and the agents partial is re-rendered in place (so
// the flipped status and Resume button show), with no flash retarget.
func TestWebAgentSuspendSuccess(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	// After suspend the list reflects the new suspended status.
	svc.records = []agent.Record{{Name: "leo-coding-leo", Status: "suspended", StartedAt: time.Now()}}

	req := httptest.NewRequest("POST", "/web/agent/leo-coding-leo/suspend", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if !svc.suspendCalled {
		t.Fatal("expected Suspend to be called")
	}
	if svc.suspendName != "leo-coding-leo" {
		t.Errorf("expected Suspend called with canonical name, got %q", svc.suspendName)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("HX-Retarget"); got != "" {
		t.Errorf("success path must not set HX-Retarget, got %q", got)
	}

	body := w.Body.String()
	if !strings.Contains(body, `id="agents-content"`) {
		t.Errorf("expected agents partial re-render, got %q", body)
	}
	// A suspended agent must offer Resume (to wake) and Stop (to terminate),
	// but not Suspend.
	if !strings.Contains(body, "/web/agent/leo-coding-leo/resume") {
		t.Errorf("expected Resume action for suspended agent, got %q", body)
	}
	if !strings.Contains(body, "/web/agent/leo-coding-leo/stop") {
		t.Errorf("expected Stop action for suspended agent, got %q", body)
	}
	if strings.Contains(body, "/web/agent/leo-coding-leo/suspend") {
		t.Errorf("suspended agent must not show a Suspend button, got %q", body)
	}
}

// TestWebAgentSuspendError asserts a failing Suspend retargets its error flash
// to the shared #flash-container rather than outerHTML-swapping the agents tab.
func TestWebAgentSuspendError(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	svc.suspendErr = fmt.Errorf("agent %q is not running", "leo-coding-leo")

	req := httptest.NewRequest("POST", "/web/agent/leo-coding-leo/suspend", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if !svc.suspendCalled {
		t.Fatal("expected Suspend to be called")
	}
	if got := w.Header().Get("HX-Retarget"); got != "#flash-container" {
		t.Errorf("expected HX-Retarget '#flash-container', got %q", got)
	}
	body := w.Body.String()
	if !strings.Contains(body, "flash-error") {
		t.Errorf("expected flash markup in body, got %q", body)
	}
	if strings.Contains(body, `id="agents-content"`) {
		t.Errorf("error response must not re-render the agents tab, got %q", body)
	}
}

// TestWebAgentResumeSuccess drives the FORM handler with a suspended agent and
// asserts Resume is called with the canonical name (Resolve is skipped because
// suspended agents are not live) and the partial is re-rendered in place.
func TestWebAgentResumeSuccess(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	svc.resumeResult = agent.Record{Name: "leo-coding-leo", Status: "running"}
	svc.records = []agent.Record{{Name: "leo-coding-leo", Status: "running", StartedAt: time.Now()}}

	req := httptest.NewRequest("POST", "/web/agent/leo-coding-leo/resume", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if !svc.resumeCalled {
		t.Fatal("expected Resume to be called")
	}
	if svc.resumeName != "leo-coding-leo" {
		t.Errorf("expected Resume called with canonical name, got %q", svc.resumeName)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("HX-Retarget"); got != "" {
		t.Errorf("success path must not set HX-Retarget, got %q", got)
	}
	body := w.Body.String()
	if !strings.Contains(body, `id="agents-content"`) {
		t.Errorf("expected agents partial re-render, got %q", body)
	}
	// A running agent offers Suspend + Stop, not Resume.
	if !strings.Contains(body, "/web/agent/leo-coding-leo/suspend") {
		t.Errorf("expected Suspend action for running agent, got %q", body)
	}
}

// TestWebAgentResumeError asserts a failing Resume retargets its error flash to
// the shared #flash-container and leaves the agents tab intact.
func TestWebAgentResumeError(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	svc.resumeErr = fmt.Errorf("no suspended agent %q", "leo-coding-leo")

	req := httptest.NewRequest("POST", "/web/agent/leo-coding-leo/resume", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if !svc.resumeCalled {
		t.Fatal("expected Resume to be called")
	}
	if got := w.Header().Get("HX-Retarget"); got != "#flash-container" {
		t.Errorf("expected HX-Retarget '#flash-container', got %q", got)
	}
	if strings.Contains(w.Body.String(), `id="agents-content"`) {
		t.Errorf("error response must not re-render the agents tab")
	}
}
