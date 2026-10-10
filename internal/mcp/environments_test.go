package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

// environmentsDaemon answers the agent list with one agent built from tmpl and
// accepts the environments POST.
func environmentsDaemon(t *testing.T, tmpl string) *fakeDaemon {
	t.Helper()
	d := newFakeDaemon(func(method, path string, _ []byte) (int, string) {
		if method == "GET" && path == "/api/agent/list" {
			return 200, `{"ok":true,"data":[{"name":"scout-1","template":"` + tmpl + `","status":"running"}]}`
		}
		return 200, `{"ok":true,"data":{"name":"scout-1","status":"running"}}`
	})
	t.Cleanup(d.close)
	return d
}

func TestSpawnAgentForwardsEnvironments(t *testing.T) {
	d := newOKDaemon(t)
	reg := newRegistry(newDaemonClient(d.port(), ""), "primary", leotools.Permissions{})
	_, err := callTool(reg, "leo_spawn_agent", map[string]any{
		"template": "codex", "environments": []any{"work", "proxy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := d.recorded()
	if len(calls) != 1 || calls[0].Path != "/api/agent/spawn" {
		t.Fatalf("calls = %+v", calls)
	}
	var body struct {
		Environments []string `json:"environments"`
	}
	if err := json.Unmarshal([]byte(calls[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	if strings.Join(body.Environments, ",") != "work,proxy" {
		t.Fatalf("body = %s", calls[0].Body)
	}
}

func TestSetAgentEnvironmentsPostsToTheAgent(t *testing.T) {
	d := environmentsDaemon(t, "codex")
	reg := newRegistry(newDaemonClient(d.port(), ""), "primary", leotools.Permissions{})
	if _, err := callTool(reg, "leo_set_agent_environments", map[string]any{
		"name": "scout-1", "environments": []any{"work"},
	}); err != nil {
		t.Fatal(err)
	}
	calls := d.recorded()
	last := calls[len(calls)-1]
	if last.Method != "POST" || last.Path != "/api/agent/scout-1/environments" || !strings.Contains(last.Body, `"work"`) {
		t.Fatalf("last call = %+v", last)
	}
}

func TestSetAgentEnvironmentsClearsWithEmptyList(t *testing.T) {
	d := environmentsDaemon(t, "codex")
	reg := newRegistry(newDaemonClient(d.port(), ""), "primary", leotools.Permissions{})
	if _, err := callTool(reg, "leo_set_agent_environments", map[string]any{"name": "scout-1"}); err != nil {
		t.Fatal(err)
	}
	calls := d.recorded()
	if last := calls[len(calls)-1]; !strings.Contains(last.Body, `"environments":[]`) {
		t.Fatalf("an omitted list must clear the override, body = %s", last.Body)
	}
}

// The caller may only re-point agents whose template it is allowed to spawn,
// otherwise a narrowed spawner could grant itself another account's credentials.
func TestSetAgentEnvironmentsRequiresSpawnRightsOnTheAgentTemplate(t *testing.T) {
	d := environmentsDaemon(t, "opus")
	reg := newRegistry(newDaemonClient(d.port(), ""), "primary", leotools.Permissions{CanSpawn: []string{"codex"}})
	_, err := callTool(reg, "leo_set_agent_environments", map[string]any{"name": "scout-1", "environments": []any{"work"}})
	if err == nil || !strings.Contains(err.Error(), "codex") {
		t.Fatalf("err = %v, want a denial listing codex", err)
	}
	for _, c := range d.recorded() {
		if c.Method == "POST" {
			t.Fatalf("a denied call must not reach the environments endpoint: %+v", c)
		}
	}
}

func TestSetAgentEnvironmentsUnknownAgent(t *testing.T) {
	d := environmentsDaemon(t, "codex")
	reg := newRegistry(newDaemonClient(d.port(), ""), "primary", leotools.Permissions{})
	_, err := callTool(reg, "leo_set_agent_environments", map[string]any{"name": "ghost"})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("err = %v", err)
	}
}

func TestSetAgentEnvironmentsHonorsDenyTools(t *testing.T) {
	d := environmentsDaemon(t, "codex")
	reg := newRegistry(newDaemonClient(d.port(), ""), "primary", leotools.Permissions{DenyTools: []string{"leo_set_agent_environments"}})
	if _, err := callTool(reg, "leo_set_agent_environments", map[string]any{"name": "scout-1"}); err == nil {
		t.Fatal("denied tool was callable")
	}
}
