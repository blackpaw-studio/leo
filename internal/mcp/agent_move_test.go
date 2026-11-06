package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

// moveDaemon is environmentsDaemon answering the move POST with a running job.
func moveDaemon(t *testing.T, tmpl string) *fakeDaemon {
	t.Helper()
	d := newFakeDaemon(func(method, path string, _ []byte) (int, string) {
		if method == "GET" && path == "/api/agent/list" {
			return 200, `{"ok":true,"data":[{"name":"scout-1","template":"` + tmpl + `","status":"running"}]}`
		}
		return 202, `{"ok":true,"data":{"id":"mv-1","state":"running"}}`
	})
	t.Cleanup(d.close)
	return d
}

func lastPost(t *testing.T, d *fakeDaemon) recordedCall {
	t.Helper()
	calls := d.recorded()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Method == "POST" {
			return calls[i]
		}
	}
	t.Fatalf("no POST reached the daemon: %+v", calls)
	return recordedCall{}
}

func TestMCPAgentMoveStartsJob(t *testing.T) {
	d := moveDaemon(t, "codex")
	reg := newRegistry(newDaemonClient(d.port(), ""), "primary", leotools.Permissions{CanSpawn: []string{"review"}})
	out, err := callTool(reg, "leo_agent_move", map[string]any{"name": "scout-1", "to": "laptop", "force": true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "mv-1") {
		t.Fatalf("result = %s, want the job id", out)
	}
	post := lastPost(t, d)
	var body map[string]any
	if err := json.Unmarshal([]byte(post.Body), &body); err != nil {
		t.Fatal(err)
	}
	if post.Path != "/api/agent/scout-1/move" || body["mode"] != "move" || body["to"] != "laptop" || body["force"] != true {
		t.Fatalf("post = %+v", post)
	}
}

func TestMCPAgentMoveNeedsTo(t *testing.T) {
	d := moveDaemon(t, "codex")
	reg := newRegistry(newDaemonClient(d.port(), ""), "primary", leotools.Permissions{})
	if _, err := callTool(reg, "leo_agent_move", map[string]any{"name": "scout-1"}); err == nil || !strings.Contains(err.Error(), `"to"`) {
		t.Fatalf("move without to = %v", err)
	}
	if len(d.recorded()) != 0 {
		t.Fatalf("a refused call reached the daemon: %+v", d.recorded())
	}
}

func TestMCPAgentCopyForwardsOptions(t *testing.T) {
	d := moveDaemon(t, "codex")
	reg := newRegistry(newDaemonClient(d.port(), ""), "primary", leotools.Permissions{})
	if _, err := callTool(reg, "leo_agent_copy", map[string]any{"name": "scout-1", "new_name": "scout-2", "branch": "b"}); err != nil {
		t.Fatal(err)
	}
	post := lastPost(t, d)
	var body map[string]any
	if err := json.Unmarshal([]byte(post.Body), &body); err != nil {
		t.Fatal(err)
	}
	if post.Path != "/api/agent/scout-1/move" || body["mode"] != "copy" || body["name"] != "scout-2" || body["branch"] != "b" || body["to"] != nil {
		t.Fatalf("post = %+v", post)
	}
}

// A copy is a spawn from the source's template, so can_spawn must cover it.
func TestMCPAgentCopyRequiresSpawnPerm(t *testing.T) {
	d := moveDaemon(t, "opus")
	reg := newRegistry(newDaemonClient(d.port(), ""), "primary", leotools.Permissions{CanSpawn: []string{"codex"}})
	for _, args := range []map[string]any{{"name": "scout-1"}, {"name": "scout-1", "to": "laptop"}} {
		if _, err := callTool(reg, "leo_agent_copy", args); err == nil || !strings.Contains(err.Error(), "codex") {
			t.Fatalf("copy %v: err = %v, want a denial listing codex", args, err)
		}
	}
	for _, c := range d.recorded() {
		if c.Method == "POST" {
			t.Fatalf("a denied copy must not reach the move endpoint: %+v", c)
		}
	}
}

// Denying leo_spawn_agent removes spawning outright, copies included.
func TestMCPAgentCopyRefusedWhenSpawnDenied(t *testing.T) {
	d := moveDaemon(t, "codex")
	reg := newRegistry(newDaemonClient(d.port(), ""), "primary", leotools.Permissions{DenyTools: []string{"leo_spawn_agent"}})
	if _, err := callTool(reg, "leo_agent_copy", map[string]any{"name": "scout-1"}); err == nil || !strings.Contains(err.Error(), "leo_spawn_agent") {
		t.Fatalf("copy with spawning denied = %v", err)
	}
	for _, c := range d.recorded() {
		if c.Method == "POST" {
			t.Fatalf("a denied copy must not reach the move endpoint: %+v", c)
		}
	}
}

// fresh_session reaches the move request on both tools.
func TestMCPAgentMoveAndCopyForwardFreshSession(t *testing.T) {
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"leo_agent_move", map[string]any{"name": "scout-1", "to": "laptop", "fresh_session": true}},
		{"leo_agent_copy", map[string]any{"name": "scout-1", "fresh_session": true}},
	} {
		d := moveDaemon(t, "codex")
		reg := newRegistry(newDaemonClient(d.port(), ""), "primary", leotools.Permissions{})
		if _, err := callTool(reg, tc.tool, tc.args); err != nil {
			t.Fatalf("%s: %v", tc.tool, err)
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(lastPost(t, d).Body), &body); err != nil {
			t.Fatal(err)
		}
		if body["fresh_session"] != true {
			t.Fatalf("%s body = %v, want fresh_session true", tc.tool, body)
		}
	}
	// Left out, it is not sent at all.
	d := moveDaemon(t, "codex")
	reg := newRegistry(newDaemonClient(d.port(), ""), "primary", leotools.Permissions{})
	if _, err := callTool(reg, "leo_agent_move", map[string]any{"name": "scout-1", "to": "laptop"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(lastPost(t, d).Body, "fresh_session") {
		t.Fatalf("body = %s, want no fresh_session by default", lastPost(t, d).Body)
	}
}

// A refused start (the daemon's 409) reaches the caller as the plain
// sentence, which says how to retry.
func TestMCPAgentMoveSurfacesSessionMissing(t *testing.T) {
	const sentence = "scout-1's saved session transcript no longer exists; retry with a fresh session"
	d := newFakeDaemon(func(method, path string, _ []byte) (int, string) {
		if method == "GET" {
			return 200, `{"ok":true,"data":[{"name":"scout-1","template":"codex","status":"running"}]}`
		}
		return 409, `{"ok":false,"error":"` + sentence + `","code":"session_missing"}`
	})
	t.Cleanup(d.close)
	reg := newRegistry(newDaemonClient(d.port(), ""), "primary", leotools.Permissions{})
	_, err := callTool(reg, "leo_agent_move", map[string]any{"name": "scout-1", "to": "laptop"})
	if err == nil || !strings.Contains(err.Error(), sentence) {
		t.Fatalf("err = %v, want %q", err, sentence)
	}
}
