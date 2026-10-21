package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
)

func getHealth(t *testing.T, client *http.Client) healthData {
	t.Helper()
	resp, err := client.Get("http://localhost/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	var env Response
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var h healthData
	if err := json.Unmarshal(env.Data, &h); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	return h
}

func TestHealthReportsClaudeLookupLive(t *testing.T) {
	s, client := startTestServerWithAgent(t, &fakeAgentManager{})
	path := ""
	s.SetClaudeLookup(func() (string, error) {
		if path == "" {
			return "", claudeharness.ErrNotFound
		}
		return path, nil
	})

	if got := getHealth(t, client).Claude; got == nil || got.Found || got.Path != "" {
		t.Fatalf("claude before install = %+v, want found:false and no path", got)
	}

	path = "/opt/bin/claude"
	got := getHealth(t, client).Claude
	if got == nil || !got.Found || got.Path != path {
		t.Fatalf("claude after install = %+v, want found:true path:%s", got, path)
	}
}

func TestHealthOmitsClaudeWithoutLookup(t *testing.T) {
	_, client := startTestServerWithAgent(t, &fakeAgentManager{})
	if got := getHealth(t, client).Claude; got != nil {
		t.Errorf("claude = %+v, want omitted when no lookup is wired", got)
	}
}

func TestAgentSpawnHandlerClaudeNotFound(t *testing.T) {
	mgr := &fakeAgentManager{spawnErr: errors.Join(errors.New("spawning agent"), agent.ErrClaudeNotFound)}
	_, client := startTestServerWithAgent(t, mgr)

	body, _ := json.Marshal(AgentSpawnRequest{Template: "coding"})
	resp, err := client.Post("http://localhost/agents/spawn", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	var env Response
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.StatusCode != http.StatusFailedDependency || env.Code != ErrorCodeClaudeNotFound {
		t.Errorf("status=%d code=%q, want 424 %q", resp.StatusCode, env.Code, ErrorCodeClaudeNotFound)
	}
}

func TestResponseErrorClaudeNotFoundRoundTrips(t *testing.T) {
	err := responseError(&Response{Error: agent.ErrClaudeNotFound.Error(), Code: ErrorCodeClaudeNotFound}, "x")
	if !errors.Is(err, agent.ErrClaudeNotFound) {
		t.Errorf("err = %v, want errors.Is agent.ErrClaudeNotFound", err)
	}
}
