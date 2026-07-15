package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/harness"
)

// fakeAgentManager is a minimal AgentManager for daemon endpoint tests.
type fakeAgentManager struct {
	records    []agent.Record
	spawnErr   error
	stopErr    error
	suspendErr error
	resumeErr  error
	resumeRec  agent.Record
	resetErr   error
	restartErr error
	restartAll agent.RestartResult
	pruneErr   error
	logsErr    error
	logsOut    string
	renameErr  error

	lastSpawn        agent.SpawnSpec
	lastStop         string
	lastSuspend      string
	lastResume       string
	lastReset        string
	lastRestart      string
	restartAllCalled bool
	lastPrune        struct {
		name string
		opts agent.PruneOptions
	}
	lastLogs struct {
		name  string
		lines int
	}
	lastRename struct {
		query   string
		newName string
	}

	// handles backs ResolveHandle for attach-spec tests: keyed by agent name,
	// maps to (harnessName, SessionHandle). A missing key means ok=false.
	handles map[string]struct {
		harnessName string
		handle      harness.SessionHandle
	}
}

func (f *fakeAgentManager) ResolveHandle(name string) (string, harness.SessionHandle, bool) {
	rh, ok := f.handles[name]
	if !ok {
		return "", harness.SessionHandle{}, false
	}
	return rh.harnessName, rh.handle, true
}

func (f *fakeAgentManager) Spawn(_ context.Context, spec agent.SpawnSpec) (agent.Record, error) {
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

func (f *fakeAgentManager) Suspend(name string) error {
	f.lastSuspend = name
	return f.suspendErr
}

func (f *fakeAgentManager) Resume(name string) (agent.Record, error) {
	f.lastResume = name
	if f.resumeErr != nil {
		return agent.Record{}, f.resumeErr
	}
	if f.resumeRec.Name != "" {
		return f.resumeRec, nil
	}
	return agent.Record{Name: name}, nil
}

func (f *fakeAgentManager) Reset(name string) error {
	f.lastReset = name
	return f.resetErr
}

func (f *fakeAgentManager) Restart(name string) error {
	f.lastRestart = name
	return f.restartErr
}

func (f *fakeAgentManager) RestartAll() agent.RestartResult {
	f.restartAllCalled = true
	return f.restartAll
}

func (f *fakeAgentManager) Prune(_ context.Context, name string, opts agent.PruneOptions) error {
	f.lastPrune.name = name
	f.lastPrune.opts = opts
	return f.pruneErr
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
	return agent.SessionName(name)
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

func (f *fakeAgentManager) Rename(query, newName string) (agent.Record, error) {
	f.lastRename.query = query
	f.lastRename.newName = newName
	if f.renameErr != nil {
		return agent.Record{}, f.renameErr
	}
	return agent.Record{Name: newName}, nil
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

func TestAgentSpawnHandlerForwardsPromptAndEnv(t *testing.T) {
	mgr := &fakeAgentManager{}
	_, client := startTestServerWithAgent(t, mgr)

	body, _ := json.Marshal(AgentSpawnRequest{
		Template: "coding",
		Repo:     "leo",
		Prompt:   "investigate alert X",
		Env:      map[string]string{"SLACK_THREAD_TS": "123.456"},
	})
	resp, err := client.Post("http://localhost/agents/spawn", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mgr.lastSpawn.Prompt != "investigate alert X" {
		t.Errorf("prompt not forwarded: spec = %+v", mgr.lastSpawn)
	}
	if mgr.lastSpawn.Env["SLACK_THREAD_TS"] != "123.456" {
		t.Errorf("env not forwarded: spec = %+v", mgr.lastSpawn)
	}
}

func TestAgentSpawnMissingFields(t *testing.T) {
	mgr := &fakeAgentManager{}
	_, client := startTestServerWithAgent(t, mgr)

	body, _ := json.Marshal(AgentSpawnRequest{})
	resp, err := client.Post("http://localhost/agents/spawn", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

func TestAgentSpawnWithoutRepoSucceeds(t *testing.T) {
	mgr := &fakeAgentManager{}
	_, client := startTestServerWithAgent(t, mgr)

	body, _ := json.Marshal(AgentSpawnRequest{Template: "coding"})
	resp, err := client.Post("http://localhost/agents/spawn", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mgr.lastSpawn.Template != "coding" || mgr.lastSpawn.Repo != "" {
		t.Errorf("spawn spec = %+v, want empty repo", mgr.lastSpawn)
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
	if out.Name != "leo-coding-acme-widget" || out.Session != "leo-coding-acme-widget" || out.Repo != "acme/widget" {
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

// --- prune handler coverage ---

func TestAgentPruneHandlerSuccess(t *testing.T) {
	mgr := &fakeAgentManager{}
	_, client := startTestServerWithAgent(t, mgr)

	body, _ := json.Marshal(AgentPruneRequest{Force: true, DeleteBranch: true})
	resp, err := client.Post("http://localhost/agents/leo-worktree/prune", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mgr.lastPrune.name != "leo-worktree" {
		t.Errorf("lastPrune.name = %q, want leo-worktree", mgr.lastPrune.name)
	}
	if !mgr.lastPrune.opts.Force || !mgr.lastPrune.opts.DeleteBranch {
		t.Errorf("lastPrune.opts = %+v, want Force+DeleteBranch", mgr.lastPrune.opts)
	}
}

func TestAgentPruneHandlerNoBody(t *testing.T) {
	// No body should default to the safest options (all false) and still succeed.
	mgr := &fakeAgentManager{}
	_, client := startTestServerWithAgent(t, mgr)

	req, _ := http.NewRequest("POST", "http://localhost/agents/leo-worktree/prune", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mgr.lastPrune.opts.Force || mgr.lastPrune.opts.DeleteBranch {
		t.Errorf("lastPrune.opts = %+v, want zero", mgr.lastPrune.opts)
	}
}

func TestAgentPruneHandlerInvalidJSON(t *testing.T) {
	mgr := &fakeAgentManager{}
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Post("http://localhost/agents/leo-worktree/prune", "application/json", bytes.NewReader([]byte("{not-json")))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

func TestAgentPruneHandlerNoManager(t *testing.T) {
	dir, _ := os.MkdirTemp("", "leo-agent-daemon-*")
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath) // no SetAgentManager

	resp, err := client.Post("http://localhost/agents/leo-worktree/prune", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", resp.StatusCode)
	}
}

// --- suspend/resume handler coverage ---

func TestAgentSuspendHandler(t *testing.T) {
	mgr := &fakeAgentManager{}
	_, client := startTestServerWithAgent(t, mgr)

	req, _ := http.NewRequest("POST", "http://localhost/agents/foo/suspend", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mgr.lastSuspend != "foo" {
		t.Errorf("lastSuspend = %q, want foo", mgr.lastSuspend)
	}
	var env Response
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !env.OK {
		t.Errorf("env.OK = false, want true")
	}
}

func TestAgentSuspendHandlerError(t *testing.T) {
	mgr := &fakeAgentManager{suspendErr: errors.New("agent not running")}
	_, client := startTestServerWithAgent(t, mgr)

	req, _ := http.NewRequest("POST", "http://localhost/agents/foo/suspend", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", resp.StatusCode)
	}
}

func TestAgentSuspendHandlerNoManager(t *testing.T) {
	dir, _ := os.MkdirTemp("", "leo-agent-daemon-*")
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath) // no SetAgentManager

	req, _ := http.NewRequest("POST", "http://localhost/agents/foo/suspend", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", resp.StatusCode)
	}
}

func TestAgentResumeHandler(t *testing.T) {
	mgr := &fakeAgentManager{resumeRec: agent.Record{Name: "foo", Template: "coding"}}
	_, client := startTestServerWithAgent(t, mgr)

	req, _ := http.NewRequest("POST", "http://localhost/agents/foo/resume", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mgr.lastResume != "foo" {
		t.Errorf("lastResume = %q, want foo", mgr.lastResume)
	}
	var env Response
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !env.OK {
		t.Errorf("env.OK = false, want true")
	}
	var rec agent.Record
	if err := json.Unmarshal(env.Data, &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rec.Name != "foo" || rec.Template != "coding" {
		t.Errorf("record = %+v, want Name=foo Template=coding", rec)
	}
}

func TestAgentResumeHandlerError(t *testing.T) {
	mgr := &fakeAgentManager{resumeErr: errors.New("agent not suspended")}
	_, client := startTestServerWithAgent(t, mgr)

	req, _ := http.NewRequest("POST", "http://localhost/agents/foo/resume", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", resp.StatusCode)
	}
}

// --- reset handler coverage ---
//
// Reset resolves its name query the same way Stop does (resolveAgentOrError
// against mgr.Resolve), unlike suspend/resume which pass the raw name
// straight through — so these tests seed mgr.records like the stop tests do.

func TestAgentResetHandler(t *testing.T) {
	mgr := &fakeAgentManager{records: []agent.Record{{Name: "foo"}}}
	_, client := startTestServerWithAgent(t, mgr)

	req, _ := http.NewRequest("POST", "http://localhost/agents/foo/reset", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mgr.lastReset != "foo" {
		t.Errorf("lastReset = %q, want foo", mgr.lastReset)
	}
	var env Response
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !env.OK {
		t.Errorf("env.OK = false, want true")
	}
}

func TestAgentResetHandlerNotFound(t *testing.T) {
	mgr := &fakeAgentManager{records: []agent.Record{}}
	_, client := startTestServerWithAgent(t, mgr)

	req, _ := http.NewRequest("POST", "http://localhost/agents/missing/reset", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestAgentResetHandlerError(t *testing.T) {
	mgr := &fakeAgentManager{
		records:  []agent.Record{{Name: "foo"}},
		resetErr: errors.New("stopping agent for reset: tmux kill failed"),
	}
	_, client := startTestServerWithAgent(t, mgr)

	req, _ := http.NewRequest("POST", "http://localhost/agents/foo/reset", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", resp.StatusCode)
	}
}

func TestAgentResetHandlerNoManager(t *testing.T) {
	dir, _ := os.MkdirTemp("", "leo-agent-daemon-*")
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath) // no SetAgentManager

	req, _ := http.NewRequest("POST", "http://localhost/agents/foo/reset", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", resp.StatusCode)
	}
}

func TestAgentRestartHandler(t *testing.T) {
	mgr := &fakeAgentManager{records: []agent.Record{{Name: "foo"}}}
	_, client := startTestServerWithAgent(t, mgr)

	req, _ := http.NewRequest("POST", "http://localhost/agents/foo/restart", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mgr.lastRestart != "foo" {
		t.Errorf("lastRestart = %q, want foo", mgr.lastRestart)
	}
	var env Response
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !env.OK {
		t.Errorf("env.OK = false, want true")
	}
}

func TestAgentRestartHandlerNotFound(t *testing.T) {
	mgr := &fakeAgentManager{records: []agent.Record{}}
	_, client := startTestServerWithAgent(t, mgr)

	req, _ := http.NewRequest("POST", "http://localhost/agents/missing/restart", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

func TestAgentRestartHandlerError(t *testing.T) {
	mgr := &fakeAgentManager{
		records:    []agent.Record{{Name: "foo"}},
		restartErr: errors.New("stopping agent for restart: tmux kill failed"),
	}
	_, client := startTestServerWithAgent(t, mgr)

	req, _ := http.NewRequest("POST", "http://localhost/agents/foo/restart", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", resp.StatusCode)
	}
}

func TestAgentRestartHandlerNoManager(t *testing.T) {
	dir, _ := os.MkdirTemp("", "leo-agent-daemon-*")
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath) // no SetAgentManager

	req, _ := http.NewRequest("POST", "http://localhost/agents/foo/restart", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", resp.StatusCode)
	}
}

func TestAgentRestartAllHandler(t *testing.T) {
	mgr := &fakeAgentManager{
		restartAll: agent.RestartResult{
			Restarted: []string{"leo-a"},
			Skipped:   []string{"leo-b"},
			Failed:    map[string]error{"leo-c": errors.New("boom")},
		},
	}
	_, client := startTestServerWithAgent(t, mgr)

	req, _ := http.NewRequest("POST", "http://localhost/agents/restart", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if !mgr.restartAllCalled {
		t.Fatal("RestartAll was not called")
	}

	var env Response
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !env.OK {
		t.Fatalf("env.OK = false, want true")
	}
	var out AgentRestartAllResponse
	if err := json.Unmarshal(env.Data, &out); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if len(out.Restarted) != 1 || out.Restarted[0] != "leo-a" {
		t.Errorf("Restarted = %v, want [leo-a]", out.Restarted)
	}
	if len(out.Skipped) != 1 || out.Skipped[0] != "leo-b" {
		t.Errorf("Skipped = %v, want [leo-b]", out.Skipped)
	}
	if out.Failed["leo-c"] != "boom" {
		t.Errorf("Failed[leo-c] = %q, want boom", out.Failed["leo-c"])
	}
}

func TestAgentRestartAllHandlerNoManager(t *testing.T) {
	dir, _ := os.MkdirTemp("", "leo-agent-daemon-*")
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath) // no SetAgentManager

	req, _ := http.NewRequest("POST", "http://localhost/agents/restart", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", resp.StatusCode)
	}
}

func TestAgentResumeHandlerNoManager(t *testing.T) {
	dir, _ := os.MkdirTemp("", "leo-agent-daemon-*")
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath) // no SetAgentManager

	req, _ := http.NewRequest("POST", "http://localhost/agents/foo/resume", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", resp.StatusCode)
	}
}

// TestAgentSpawnForwardsIdleSuspend verifies that idle_suspend is threaded
// through the spawn request into the SpawnSpec.
func TestAgentSpawnForwardsIdleSuspend(t *testing.T) {
	mgr := &fakeAgentManager{}
	_, client := startTestServerWithAgent(t, mgr)

	body, _ := json.Marshal(AgentSpawnRequest{
		Template:    "coding",
		Repo:        "leo",
		IdleSuspend: "4h",
	})
	resp, err := client.Post("http://localhost/agents/spawn", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mgr.lastSpawn.IdleSuspend != "4h" {
		t.Errorf("lastSpawn.IdleSuspend = %q, want 4h", mgr.lastSpawn.IdleSuspend)
	}
}

// TestAgentPruneHandlerErrorCodes verifies that each typed error from the
// agent package maps to the stable (status, code) pair the CLI client relies
// on for errors.Is dispatch.
func TestAgentPruneHandlerErrorCodes(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"dirty", agent.ErrWorktreeDirty, http.StatusConflict, ErrorCodeWorktreeDirty},
		{"not_merged", agent.ErrBranchNotMerged, http.StatusConflict, ErrorCodeBranchNotMerged},
		{"still_running", agent.ErrAgentStillRunning, http.StatusConflict, ErrorCodeAgentStillRunning},
		{"not_worktree", agent.ErrNotWorktreeAgent, http.StatusBadRequest, ErrorCodeNotWorktreeAgent},
		{"requires_slash", agent.ErrWorktreeRequiresSlash, http.StatusBadRequest, ErrorCodeWorktreeRequireSep},
		{"branch_checked_out", agent.ErrBranchCheckedOut, http.StatusConflict, ErrorCodeBranchCheckedOut},
		{"branch_not_found", agent.ErrBranchNotFound, http.StatusNotFound, ErrorCodeBranchNotFound},
		{"unknown", errors.New("boom"), http.StatusInternalServerError, ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			mgr := &fakeAgentManager{pruneErr: tc.err}
			_, client := startTestServerWithAgent(t, mgr)

			body, _ := json.Marshal(AgentPruneRequest{})
			resp, err := client.Post("http://localhost/agents/leo-worktree/prune", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("%s: status = %d, want %d", tc.name, resp.StatusCode, tc.wantStatus)
			}
			if tc.wantCode == "" {
				return
			}
			var env Response
			if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if env.Code != tc.wantCode {
				t.Errorf("%s: code = %q, want %q", tc.name, env.Code, tc.wantCode)
			}
			if env.OK {
				t.Errorf("%s: env.OK should be false on error", tc.name)
			}
		})
	}
}
