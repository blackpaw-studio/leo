package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
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

	resumeCalled bool
	resumeName   string
	resumeResult agent.Record
	resumeErr    error

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

// TestProcessMessageAutoWakesSuspendedAgent verifies that when a message is
// targeted at a name that is NOT in the live process states but IS a suspended
// agent, handleProcessMessage calls Resume and then delivers via the live
// send-keys path (200 OK, Resume was called, tmux send-keys executed).
func TestProcessMessageAutoWakesSuspendedAgent(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)

	oldPoll, oldAttempts := messageInputPoll, messageInputAttempts
	messageInputPoll, messageInputAttempts = time.Millisecond, 2
	defer func() { messageInputPoll, messageInputAttempts = oldPoll, oldAttempts }()

	// "suspended-worker" is NOT in live states — it should be resumed then
	// messaged via the live send-keys path.
	svc.resumeResult = agent.Record{Name: "suspended-worker", Status: "starting"}

	var calls [][]string
	s.execCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, args)
		return exec.Command("true")
	}

	body := strings.NewReader(`{"text":"wake up and do the thing"}`)
	req := httptest.NewRequest("POST", "/web/process/suspended-worker/message", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !svc.resumeCalled {
		t.Fatal("expected Resume to be called for suspended agent")
	}
	if svc.resumeName != "suspended-worker" {
		t.Errorf("expected Resume called with 'suspended-worker', got %q", svc.resumeName)
	}
	// The live delivery path must also have fired (send-keys).
	found := false
	for _, call := range calls {
		if argsContain(call, "send-keys") && argsContain(call, "-l") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected send-keys literal call for message delivery; got calls=%v", calls)
	}
}

// TestProcessMessageUnknownTargetWithAgentServiceStill404 verifies that a name
// that is neither live NOR a suspended agent returns 404, even when agentSvc is
// set (Resume returns an error for truly unknown names).
func TestProcessMessageUnknownTargetWithAgentServiceStill404(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)

	// Make Resume fail — name is not suspended, so it's unknown.
	svc.resumeErr = fmt.Errorf("agent %q is not suspended", "ghost")

	body := strings.NewReader(`{"text":"hello"}`)
	req := httptest.NewRequest("POST", "/web/process/ghost/message", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", w.Code, w.Body.String())
	}
	if !svc.resumeCalled {
		t.Fatal("expected Resume to be attempted for unknown target")
	}
	body2 := w.Body.String()
	if !strings.Contains(body2, "ghost") {
		t.Errorf("404 body should mention the unknown name; got %s", body2)
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
