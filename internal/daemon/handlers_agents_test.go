package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
)

// fakeAgentManager is a minimal AgentManager for daemon endpoint tests.
type fakeAgentManager struct {
	records  []agent.Record
	spawnErr error
	stopErr  error
	logsErr  error
	logsOut  string

	lastSpawn agent.SpawnSpec
	lastStop  string
	lastLogs  struct {
		name  string
		lines int
	}
}

func (f *fakeAgentManager) Spawn(spec agent.SpawnSpec) (agent.Record, error) {
	f.lastSpawn = spec
	if f.spawnErr != nil {
		return agent.Record{}, f.spawnErr
	}
	return agent.Record{Name: "leo-" + spec.Template + "-" + spec.Repo, Template: spec.Template}, nil
}

func (f *fakeAgentManager) Stop(name string) error {
	f.lastStop = name
	return f.stopErr
}

func (f *fakeAgentManager) List() []agent.Record {
	return f.records
}

func (f *fakeAgentManager) Logs(name string, lines int) (string, error) {
	f.lastLogs.name = name
	f.lastLogs.lines = lines
	return f.logsOut, f.logsErr
}

func (f *fakeAgentManager) SessionName(name string) string {
	return "leo-" + name
}

// Resolve does simple exact-name matching against the fake's records so tests
// exercising the shorthand path can stick to canonical names. Real matching
// logic is covered by internal/agent/resolve_test.go.
func (f *fakeAgentManager) Resolve(query string) (agent.Record, error) {
	for _, rec := range f.records {
		if rec.Name == query {
			return rec, nil
		}
	}
	return agent.Record{}, &agent.ErrNotFound{Query: query}
}

func startTestServerWithAgent(t *testing.T, mgr AgentManager) (*Server, *http.Client) {
	t.Helper()
	dir, err := os.MkdirTemp("", "leo-agent-daemon-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfgPath := writeTestConfig(t, dir)

	s, client := startTestServer(t, cfgPath)
	s.SetAgentManager(mgr)
	return s, client
}

func TestAgentSpawnHandler(t *testing.T) {
	mgr := &fakeAgentManager{}
	_, client := startTestServerWithAgent(t, mgr)

	body, _ := json.Marshal(AgentSpawnRequest{Template: "coding", Repo: "leo"})
	resp, err := client.Post("http://localhost/agents/spawn", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mgr.lastSpawn.Template != "coding" || mgr.lastSpawn.Repo != "leo" {
		t.Errorf("spawn spec = %+v", mgr.lastSpawn)
	}
}

func TestAgentSpawnMissingFields(t *testing.T) {
	mgr := &fakeAgentManager{}
	_, client := startTestServerWithAgent(t, mgr)

	body, _ := json.Marshal(AgentSpawnRequest{Template: "coding"})
	resp, err := client.Post("http://localhost/agents/spawn", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

func TestAgentSpawnNoManager(t *testing.T) {
	dir, _ := os.MkdirTemp("", "leo-agent-daemon-*")
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath) // no SetAgentManager

	body, _ := json.Marshal(AgentSpawnRequest{Template: "coding", Repo: "leo"})
	resp, err := client.Post("http://localhost/agents/spawn", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", resp.StatusCode)
	}
}

func TestAgentListHandler(t *testing.T) {
	mgr := &fakeAgentManager{records: []agent.Record{{Name: "a"}, {Name: "b"}}}
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Get("http://localhost/agents/list")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var env Response
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var records []agent.Record
	if err := json.Unmarshal(env.Data, &records); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(records) != 2 {
		t.Errorf("want 2 records, got %d", len(records))
	}
}

func TestAgentStopHandler(t *testing.T) {
	mgr := &fakeAgentManager{records: []agent.Record{{Name: "foo"}}}
	_, client := startTestServerWithAgent(t, mgr)

	req, _ := http.NewRequest("POST", "http://localhost/agents/foo/stop", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mgr.lastStop != "foo" {
		t.Errorf("lastStop = %q", mgr.lastStop)
	}
}

func TestAgentStopHandlerNotFound(t *testing.T) {
	mgr := &fakeAgentManager{records: []agent.Record{}}
	_, client := startTestServerWithAgent(t, mgr)

	req, _ := http.NewRequest("POST", "http://localhost/agents/missing/stop", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestAgentLogsHandler(t *testing.T) {
	mgr := &fakeAgentManager{
		records: []agent.Record{{Name: "foo"}},
		logsOut: "hello logs",
	}
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Get("http://localhost/agents/foo/logs?lines=50")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mgr.lastLogs.name != "foo" || mgr.lastLogs.lines != 50 {
		t.Errorf("lastLogs = %+v", mgr.lastLogs)
	}
	var env Response
	json.NewDecoder(resp.Body).Decode(&env) //nolint:errcheck
	var out AgentLogsResponse
	json.Unmarshal(env.Data, &out) //nolint:errcheck
	if out.Output != "hello logs" {
		t.Errorf("output = %q", out.Output)
	}
}

func TestAgentSessionHandler(t *testing.T) {
	mgr := &fakeAgentManager{records: []agent.Record{{Name: "foo"}}}
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Get("http://localhost/agents/foo/session")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var env Response
	json.NewDecoder(resp.Body).Decode(&env) //nolint:errcheck
	var out AgentSessionResponse
	json.Unmarshal(env.Data, &out) //nolint:errcheck
	if out.Session != "leo-foo" {
		t.Errorf("session = %q", out.Session)
	}
}

func TestAgentSessionNotFound(t *testing.T) {
	mgr := &fakeAgentManager{records: []agent.Record{}}
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Get("http://localhost/agents/missing/session")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

// resolveFakeAgentManager returns a pre-canned Resolve result so we can drive
// the shorthand endpoint without reimplementing the real matching algorithm.
type resolveFakeAgentManager struct {
	fakeAgentManager
	resolveOut agent.Record
	resolveErr error
}

func (r *resolveFakeAgentManager) Resolve(string) (agent.Record, error) {
	return r.resolveOut, r.resolveErr
}

func TestAgentResolveHandlerSuccess(t *testing.T) {
	mgr := &resolveFakeAgentManager{resolveOut: agent.Record{Name: "leo-coding-acme-widget", Repo: "acme/widget"}}
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Get("http://localhost/agents/resolve?q=widget")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var env Response
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var out AgentResolveResponse
	if err := json.Unmarshal(env.Data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Name != "leo-coding-acme-widget" || out.Session != "leo-leo-coding-acme-widget" || out.Repo != "acme/widget" {
		t.Errorf("resolve = %+v", out)
	}
}

func TestAgentResolveHandlerMissingQuery(t *testing.T) {
	mgr := &resolveFakeAgentManager{}
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Get("http://localhost/agents/resolve")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

func TestAgentResolveHandlerAmbiguous(t *testing.T) {
	mgr := &resolveFakeAgentManager{resolveErr: &agent.ErrAmbiguous{Query: "leo", Matches: []string{"a", "b"}}}
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Get("http://localhost/agents/resolve?q=leo")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("want 409, got %d", resp.StatusCode)
	}
}

// --- stop/logs/session conflict and error coverage ---

func TestAgentStopHandlerAmbiguous(t *testing.T) {
	mgr := &resolveFakeAgentManager{resolveErr: &agent.ErrAmbiguous{Query: "leo", Matches: []string{"a", "b"}}}
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Post("http://localhost/agents/leo/stop", "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("want 409, got %d", resp.StatusCode)
	}
}

func TestAgentStopHandlerSupervisorError(t *testing.T) {
	mgr := &resolveFakeAgentManager{resolveOut: agent.Record{Name: "leo-coding-acme-widget"}}
	mgr.stopErr = errors.New("supervisor offline")
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Post("http://localhost/agents/widget/stop", "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", resp.StatusCode)
	}
}

func TestAgentLogsHandlerAmbiguous(t *testing.T) {
	mgr := &resolveFakeAgentManager{resolveErr: &agent.ErrAmbiguous{Query: "leo", Matches: []string{"a", "b"}}}
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Get("http://localhost/agents/leo/logs")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("want 409, got %d", resp.StatusCode)
	}
}

func TestAgentLogsHandlerSupervisorError(t *testing.T) {
	mgr := &resolveFakeAgentManager{resolveOut: agent.Record{Name: "leo-coding-acme-widget"}}
	mgr.logsErr = errors.New("capture failed")
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Get("http://localhost/agents/widget/logs")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", resp.StatusCode)
	}
}

func TestAgentSessionHandlerAmbiguous(t *testing.T) {
	mgr := &resolveFakeAgentManager{resolveErr: &agent.ErrAmbiguous{Query: "leo", Matches: []string{"a", "b"}}}
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Get("http://localhost/agents/leo/session")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("want 409, got %d", resp.StatusCode)
	}
}

func TestAgentSessionHandlerNotFound(t *testing.T) {
	mgr := &resolveFakeAgentManager{resolveErr: &agent.ErrNotFound{Query: "leo"}}
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Get("http://localhost/agents/leo/session")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestAgentSpawnHandlerSupervisorError(t *testing.T) {
	mgr := &fakeAgentManager{spawnErr: errors.New("template missing")}
	_, client := startTestServerWithAgent(t, mgr)

	body, _ := json.Marshal(AgentSpawnRequest{Template: "coding", Repo: "leo"})
	resp, err := client.Post("http://localhost/agents/spawn", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", resp.StatusCode)
	}
}
