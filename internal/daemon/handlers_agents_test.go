package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/harness"
)

// fakeAgentManager is a minimal AgentManager for daemon endpoint tests.
type fakeAgentManager struct {
	lastSwitch [2]string
	switchErr  error
	records    []agent.Record
	spawnErr   error
	stopErr    error
	suspendErr error
	resumeErr  error
	resetErr   error
	restartErr error
	restartAll agent.RestartResult
	stale      []agent.StaleAgent
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
	staleCalled      bool
	lastPrune        struct {
		name string
		opts agent.DeleteOptions
	}
	lastStopOpts agent.StopOptions
	lastLogs     struct {
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

func (f *fakeAgentManager) Stop(name string, opts agent.StopOptions) error {
	f.lastStop = name
	f.lastStopOpts = opts
	if opts.WakeOnMessage {
		f.lastSuspend = name
		return f.suspendErr
	}
	return f.stopErr
}

func (f *fakeAgentManager) Start(name string) error {
	f.lastResume = name
	return f.resumeErr
}

func (f *fakeAgentManager) Reset(name string) error {
	f.lastReset = name
	return f.resetErr
}

func (f *fakeAgentManager) Restart(name string) error {
	f.lastRestart = name
	return f.restartErr
}

func (f *fakeAgentManager) ResolveRecoverable(string) (agent.Record, bool) {
	return agent.Record{}, false
}

func (f *fakeAgentManager) SwitchTemplate(name, template string) (agent.SwitchResult, error) {
	f.lastSwitch = [2]string{name, template}
	if f.switchErr != nil {
		return agent.SwitchResult{}, f.switchErr
	}
	return agent.SwitchResult{
		Name: name, FromTemplate: "coding", ToTemplate: template,
		FromHarness: "claude", ToHarness: "codex", Status: "running",
	}, nil
}

func (f *fakeAgentManager) RestartAll() agent.RestartResult {
	f.restartAllCalled = true
	return f.restartAll
}

func (f *fakeAgentManager) StaleAgents() []agent.StaleAgent {
	f.staleCalled = true
	return f.stale
}

func (f *fakeAgentManager) Delete(_ context.Context, name string, opts agent.DeleteOptions) error {
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

func TestAgentSpawnHandlerForwardsFromAgent(t *testing.T) {
	mgr := &fakeAgentManager{}
	_, client := startTestServerWithAgent(t, mgr)

	body, _ := json.Marshal(AgentSpawnRequest{FromAgent: "chronicle", Branch: "a11y"})
	resp, err := client.Post("http://localhost/agents/spawn", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mgr.lastSpawn.FromAgent != "chronicle" || mgr.lastSpawn.Branch != "a11y" {
		t.Errorf("spawn spec = %+v, want FromAgent=chronicle Branch=a11y", mgr.lastSpawn)
	}
}

func TestAgentSpawnMissingTemplateAndFromAgent(t *testing.T) {
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
	var env Response
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Error != "template or from_agent is required" {
		t.Errorf("error = %q, want %q", env.Error, "template or from_agent is required")
	}
}

func TestAgentSpawnHandlerSourceAgentNotFound(t *testing.T) {
	mgr := &fakeAgentManager{spawnErr: agent.ErrSourceAgentNotFound}
	_, client := startTestServerWithAgent(t, mgr)

	body, _ := json.Marshal(AgentSpawnRequest{FromAgent: "ghost", Branch: "x"})
	resp, err := client.Post("http://localhost/agents/spawn", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
	var env Response
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Code != ErrorCodeSourceAgentNotFound {
		t.Errorf("code = %q, want %q", env.Code, ErrorCodeSourceAgentNotFound)
	}
}

func TestAgentSpawnHandlerSourceNotGitRepo(t *testing.T) {
	mgr := &fakeAgentManager{spawnErr: agent.ErrSourceNotGitRepo}
	_, client := startTestServerWithAgent(t, mgr)

	body, _ := json.Marshal(AgentSpawnRequest{FromAgent: "plain", Branch: "x"})
	resp, err := client.Post("http://localhost/agents/spawn", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
	var env Response
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Code != ErrorCodeSourceNotGitRepo {
		t.Errorf("code = %q, want %q", env.Code, ErrorCodeSourceNotGitRepo)
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

// TestAgentRestartHandlerStopped reproduces the finding-2 regression: once
// Manager.Resolve started matching live agents only, restarting a dormant one
// no longer dies at 404 — it resolves, then Manager.Restart itself rejects a
// not-currently-running agent. That must surface as a 4xx telling the caller
// to start it first, not a bare 500.
func TestAgentRestartHandlerStopped(t *testing.T) {
	mgr := &resolveFakeAgentManager{resolveOut: agent.Record{Name: "leo-coding-acme-widget", Status: "stopped"}}
	mgr.restartErr = fmt.Errorf("%w: agent %q is stopped", agent.ErrAgentStopped, "leo-coding-acme-widget")
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Post("http://localhost/agents/widget/restart", "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("want 409, got %d", resp.StatusCode)
	}
	var env Response
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Code != ErrorCodeAgentStopped {
		t.Errorf("code = %q, want %q", env.Code, ErrorCodeAgentStopped)
	}
	if !strings.Contains(env.Error, "leo-coding-acme-widget") || !strings.Contains(env.Error, "stopped") {
		t.Errorf("error message = %q, want it to name the agent and say it is stopped", env.Error)
	}
}

// TestAgentRestartHandlerReachesFailedRestoreRecord verifies handleAgentRestart
// reaches a failed-restore record directly through Resolve, which matches
// every dormant record exactly like a live one — a shared-workspace agent
// RestoreAgents left behind after a failed boot-time restore is reachable via
// `leo agent restart <name>` with no separate fallback needed.
func TestAgentRestartHandlerReachesFailedRestoreRecord(t *testing.T) {
	mgr := &resolveFakeAgentManager{
		resolveOut: agent.Record{Name: "leo-coding-acme-widget", Status: "stopped"},
	}
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Post("http://localhost/agents/widget/restart", "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mgr.lastRestart != "leo-coding-acme-widget" {
		t.Errorf("Restart called with %q, want the resolved canonical name", mgr.lastRestart)
	}
}

// TestAgentRestartHandlerNotFoundWithoutFallback verifies a genuinely unknown
// agent still 404s.
func TestAgentRestartHandlerNotFoundWithoutFallback(t *testing.T) {
	mgr := &resolveFakeAgentManager{resolveErr: &agent.ErrNotFound{Query: "ghost"}}
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Post("http://localhost/agents/ghost/restart", "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

// TestAgentStopHandlerReachesFailedRestoreRecord is the Stop analogue of
// TestAgentRestartHandlerReachesFailedRestoreRecord: a shared-workspace agent
// RestoreAgents left Stopped+StoppedReason after a failed boot-time restore
// has no live process to kill, but is still removable via `leo agent stop
// <name>` — Resolve reaches it directly, so it never becomes an undeletable
// entry in `leo agent list`.
func TestAgentStopHandlerReachesFailedRestoreRecord(t *testing.T) {
	mgr := &resolveFakeAgentManager{
		resolveOut: agent.Record{Name: "leo-coding-acme-widget", Status: "stopped"},
	}
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Post("http://localhost/agents/widget/stop", "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mgr.lastStop != "leo-coding-acme-widget" {
		t.Errorf("Stop called with %q, want the resolved canonical name", mgr.lastStop)
	}
}

// TestAgentStopHandlerNotFoundWithoutFallback verifies a genuinely unknown
// agent still 404s.
func TestAgentStopHandlerNotFoundWithoutFallback(t *testing.T) {
	mgr := &resolveFakeAgentManager{resolveErr: &agent.ErrNotFound{Query: "ghost"}}
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Post("http://localhost/agents/ghost/stop", "application/json", nil)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

// TestAgentLogsHandlerStopped is the Logs analogue of
// TestAgentRestartHandlerStopped — see its comment.
func TestAgentLogsHandlerStopped(t *testing.T) {
	mgr := &resolveFakeAgentManager{resolveOut: agent.Record{Name: "leo-coding-acme-widget", Status: "stopped"}}
	mgr.logsErr = fmt.Errorf("%w: agent %q is stopped", agent.ErrAgentStopped, "leo-coding-acme-widget")
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Get("http://localhost/agents/widget/logs")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("want 409, got %d", resp.StatusCode)
	}
	var env Response
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Code != ErrorCodeAgentStopped {
		t.Errorf("code = %q, want %q", env.Code, ErrorCodeAgentStopped)
	}
	if !strings.Contains(env.Error, "leo-coding-acme-widget") || !strings.Contains(env.Error, "stopped") {
		t.Errorf("error message = %q, want it to name the agent and say it is stopped", env.Error)
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

// --- delete handler coverage ---

func TestAgentDeleteHandlerSuccess(t *testing.T) {
	mgr := &fakeAgentManager{}
	_, client := startTestServerWithAgent(t, mgr)

	body, _ := json.Marshal(AgentDeleteRequest{Force: true, DeleteBranch: true})
	req, _ := http.NewRequest("DELETE", "http://localhost/agents/leo-worktree", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
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

func TestAgentDeleteHandlerNoBody(t *testing.T) {
	// No body should default to the safest options (all false) and still succeed.
	mgr := &fakeAgentManager{}
	_, client := startTestServerWithAgent(t, mgr)

	req, _ := http.NewRequest("DELETE", "http://localhost/agents/leo-worktree", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mgr.lastPrune.opts.Force || mgr.lastPrune.opts.DeleteBranch {
		t.Errorf("lastPrune.opts = %+v, want zero", mgr.lastPrune.opts)
	}
}

func TestAgentDeleteHandlerInvalidJSON(t *testing.T) {
	mgr := &fakeAgentManager{}
	_, client := startTestServerWithAgent(t, mgr)

	req, _ := http.NewRequest("DELETE", "http://localhost/agents/leo-worktree", bytes.NewReader([]byte("{not-json")))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

func TestAgentDeleteHandlerNoManager(t *testing.T) {
	dir, _ := os.MkdirTemp("", "leo-agent-daemon-*")
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath) // no SetAgentManager

	req, _ := http.NewRequest("DELETE", "http://localhost/agents/leo-worktree", bytes.NewReader([]byte("{}")))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", resp.StatusCode)
	}
}

// --- stop(wake_on_message)/start handler coverage ---

func TestAgentStopHandlerWakeOnMessage(t *testing.T) {
	mgr := &fakeAgentManager{records: []agent.Record{{Name: "foo"}}}
	_, client := startTestServerWithAgent(t, mgr)

	body, _ := json.Marshal(AgentStopRequest{WakeOnMessage: true})
	req, _ := http.NewRequest("POST", "http://localhost/agents/foo/stop", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mgr.lastStop != "foo" || !mgr.lastStopOpts.WakeOnMessage {
		t.Errorf("lastStop = %q lastStopOpts = %+v, want foo/WakeOnMessage=true", mgr.lastStop, mgr.lastStopOpts)
	}
	var env Response
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !env.OK {
		t.Errorf("env.OK = false, want true")
	}
}

func TestAgentStopHandlerError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"already stopped", fmt.Errorf("%w: %q", agent.ErrAgentStopped, "foo"), http.StatusConflict},
		{"stored but not running", fmt.Errorf("%w: %q", agent.ErrAgentNotRunning, "foo"), http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := &fakeAgentManager{stopErr: tt.err, records: []agent.Record{{Name: "foo"}}}
			_, client := startTestServerWithAgent(t, mgr)

			req, _ := http.NewRequest("POST", "http://localhost/agents/foo/stop", nil)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantCode {
				t.Fatalf("want %d, got %d", tt.wantCode, resp.StatusCode)
			}
		})
	}
}

func TestAgentStartHandler(t *testing.T) {
	mgr := &fakeAgentManager{}
	_, client := startTestServerWithAgent(t, mgr)

	req, _ := http.NewRequest("POST", "http://localhost/agents/foo/start", nil)
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
}

func TestAgentStartHandlerError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"not stopped", fmt.Errorf("%w: %q", agent.ErrAgentNotStopped, "foo"), http.StatusConflict},
		{"already running", fmt.Errorf("%w: %q", agent.ErrAgentAlreadyRunning, "foo"), http.StatusConflict},
		{"unknown agent", &agent.ErrNotFound{Query: "foo"}, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := &fakeAgentManager{resumeErr: tt.err}
			_, client := startTestServerWithAgent(t, mgr)

			req, _ := http.NewRequest("POST", "http://localhost/agents/foo/start", nil)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantCode {
				t.Fatalf("want %d, got %d", tt.wantCode, resp.StatusCode)
			}
		})
	}
}

func TestAgentStartHandlerNoManager(t *testing.T) {
	dir, _ := os.MkdirTemp("", "leo-agent-daemon-*")
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath) // no SetAgentManager

	req, _ := http.NewRequest("POST", "http://localhost/agents/foo/start", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", resp.StatusCode)
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

// TestAgentStaleHandler covers GET /agents/stale, which `leo update` calls
// after a binary swap to decide which agents to offer a restart for.
func TestAgentStaleHandler(t *testing.T) {
	mgr := &fakeAgentManager{
		stale: []agent.StaleAgent{
			{Name: "leo-a", EnvAdded: []string{"MCP_TOOL_TIMEOUT"}},
			{Name: "leo-b", ArgsChanged: []string{"--model sonnet -> opus"}},
		},
	}
	_, client := startTestServerWithAgent(t, mgr)

	req, _ := http.NewRequest("GET", "http://localhost/agents/stale", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if !mgr.staleCalled {
		t.Fatal("StaleAgents was not called")
	}

	var env Response
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var out []agent.StaleAgent
	if err := json.Unmarshal(env.Data, &out); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if len(out) != 2 || out[0].Name != "leo-a" {
		t.Fatalf("stale = %+v, want two entries starting with leo-a", out)
	}
	if len(out[0].EnvAdded) != 1 || out[0].EnvAdded[0] != "MCP_TOOL_TIMEOUT" {
		t.Errorf("EnvAdded = %v", out[0].EnvAdded)
	}
	if len(out[1].ArgsChanged) != 1 || out[1].ArgsChanged[0] != "--model sonnet -> opus" {
		t.Errorf("ArgsChanged = %v", out[1].ArgsChanged)
	}
}

// TestAgentStaleHandlerEmpty: no drift serializes as an empty list, not null,
// so the CLI can range over it without a nil check.
func TestAgentStaleHandlerEmpty(t *testing.T) {
	_, client := startTestServerWithAgent(t, &fakeAgentManager{})

	req, _ := http.NewRequest("GET", "http://localhost/agents/stale", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	var env Response
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var out []agent.StaleAgent
	if err := json.Unmarshal(env.Data, &out); err != nil {
		t.Fatalf("decode data: %v (raw %s)", err, env.Data)
	}
	if len(out) != 0 {
		t.Fatalf("want no stale agents, got %+v", out)
	}
}

func TestAgentStaleHandlerNoManager(t *testing.T) {
	dir, _ := os.MkdirTemp("", "leo-agent-daemon-*")
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath) // no SetAgentManager

	req, _ := http.NewRequest("GET", "http://localhost/agents/stale", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", resp.StatusCode)
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
func TestAgentDeleteHandlerErrorCodes(t *testing.T) {
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

			body, _ := json.Marshal(AgentDeleteRequest{})
			req, _ := http.NewRequest("DELETE", "http://localhost/agents/leo-worktree", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("delete: %v", err)
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

// The set-template route resolves the name like the other lifecycle routes
// (shorthand resolution itself lives in the manager), forwards the template
// from the query string, and returns the switch result so the CLI can report
// which conversation came back.
func TestAgentSetTemplateHandler(t *testing.T) {
	mgr := &fakeAgentManager{records: []agent.Record{{Name: "leo-coding-owner-fetch"}}}
	_, client := startTestServerWithAgent(t, mgr)

	req, _ := http.NewRequest("POST", "http://localhost/agents/leo-coding-owner-fetch/set-template?template=codex", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if mgr.lastSwitch != [2]string{"leo-coding-owner-fetch", "codex"} {
		t.Fatalf("manager called with %v, want the canonical name and the requested template", mgr.lastSwitch)
	}

	var body Response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var result agent.SwitchResult
	if err := json.Unmarshal(body.Data, &result); err != nil {
		t.Fatalf("decode switch result: %v", err)
	}
	if result.ToTemplate != "codex" || result.ToHarness != "codex" {
		t.Errorf("result = %+v, want the target template and harness", result)
	}
}

func TestAgentSetTemplateHandlerRequiresTemplate(t *testing.T) {
	mgr := &fakeAgentManager{records: []agent.Record{{Name: "foo"}}}
	_, client := startTestServerWithAgent(t, mgr)

	req, _ := http.NewRequest("POST", "http://localhost/agents/foo/set-template", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400 when no template is given, got %d", resp.StatusCode)
	}
	if mgr.lastSwitch != [2]string{} {
		t.Errorf("manager was called despite a missing template: %v", mgr.lastSwitch)
	}
}

func TestAgentSetTemplateHandlerError(t *testing.T) {
	mgr := &fakeAgentManager{
		records:   []agent.Record{{Name: "foo"}},
		switchErr: errors.New("no template \"ghost\" in config"),
	}
	_, client := startTestServerWithAgent(t, mgr)

	req, _ := http.NewRequest("POST", "http://localhost/agents/foo/set-template?template=ghost", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", resp.StatusCode)
	}
}
