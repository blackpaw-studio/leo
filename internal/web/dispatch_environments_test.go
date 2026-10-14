package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/consult"
)

// newEnvDispatchServer serves a config with two environments and an agent
// "caller" (template coding) that runs under acct-b.
func newEnvDispatchServer(t *testing.T) *Server {
	t.Helper()
	s, dir, svc := newTestServerWithAgents(t)
	yaml := testConfigWithTemplatesYAML + "\nenvironments:\n  acct-b:\n    CLAUDE_CONFIG_DIR: /b\n  acct-c:\n    CLAUDE_CONFIG_DIR: /c\n"
	if err := os.WriteFile(filepath.Join(dir, "leo.yaml"), []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	svc.records = append(svc.records, agent.Record{Name: "caller", Template: "coding", Status: "running", StartedAt: time.Now(), Environments: []string{"acct-b"}})
	s.consults.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"done","is_error":false}`)
	}
	return s
}

func dispatchRecord(t *testing.T, s *Server, w interface{ Bytes() []byte }) consult.Record {
	t.Helper()
	var started struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	rec, err := s.consults.Get(started.Data.ID)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestAPIDispatchInheritsTheCallingAgentsEnvironments(t *testing.T) {
	s := newEnvDispatchServer(t)
	w := startDispatch(t, s, `{"from":"caller","template":"coding","prompt":"work","cwd":"/tmp","mode":"headless"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	rec := dispatchRecord(t, s, w.Body)
	if !reflect.DeepEqual(rec.Environments, []string{"acct-b"}) || rec.EnvironmentsSource != consult.EnvironmentsInherited {
		t.Fatalf("environments=%v source=%q", rec.Environments, rec.EnvironmentsSource)
	}
}

func TestAPIDispatchEnvironmentsOverride(t *testing.T) {
	s := newEnvDispatchServer(t)
	w := startDispatch(t, s, `{"from":"caller","template":"coding","prompt":"work","cwd":"/tmp","mode":"headless","environments":["acct-c"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	rec := dispatchRecord(t, s, w.Body)
	if !reflect.DeepEqual(rec.Environments, []string{"acct-c"}) || rec.EnvironmentsSource != consult.EnvironmentsOverride {
		t.Fatalf("environments=%v source=%q", rec.Environments, rec.EnvironmentsSource)
	}

	w = startDispatch(t, s, `{"from":"caller","template":"coding","prompt":"work","cwd":"/tmp","mode":"headless","environments":[]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	if rec := dispatchRecord(t, s, w.Body); len(rec.Environments) != 0 || rec.EnvironmentsSource != consult.EnvironmentsOverride {
		t.Fatalf("empty override: environments=%v source=%q", rec.Environments, rec.EnvironmentsSource)
	}
}

func TestAPIDispatchRejectsAnUnknownEnvironment(t *testing.T) {
	s := newEnvDispatchServer(t)
	w := startDispatch(t, s, `{"from":"caller","template":"coding","prompt":"work","cwd":"/tmp","mode":"headless","environments":["nope"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
	}
}

// A one-shot task's leo MCP server calls as "task:<name>"; its dispatches
// inherit the task's environments like an agent's do.
func TestAPIDispatchInheritsAOneShotTasksEnvironments(t *testing.T) {
	s := newEnvDispatchServer(t)
	cfgPath := s.configPath
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	yaml := strings.Replace(string(raw), "    prompt_file: heartbeat.md\n", "    prompt_file: heartbeat.md\n    environments: [acct-c]\n", 1)
	if err := os.WriteFile(cfgPath, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	w := startDispatch(t, s, `{"from":"task:heartbeat","template":"coding","prompt":"work","cwd":"/tmp","mode":"headless"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	rec := dispatchRecord(t, s, w.Body)
	if !reflect.DeepEqual(rec.Environments, []string{"acct-c"}) || rec.EnvironmentsSource != consult.EnvironmentsInherited {
		t.Fatalf("environments=%v source=%q", rec.Environments, rec.EnvironmentsSource)
	}
}
