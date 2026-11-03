package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

const progressReply = `{"ok":true,"task":{"id":"build","title":"Xcode build","mode":"determinate","percent":62,"state":"running","stale":false,"started_at":"2026-11-03T19:00:00Z","updated_at":"2026-11-03T19:01:00Z","ended_at":null,"revision":2}}`

func progressDaemon(t *testing.T, status int, reply string) *fakeDaemon {
	t.Helper()
	d := newFakeDaemon(func(method, path string, _ []byte) (int, string) {
		if method == "POST" && path == "/api/agent/progress" {
			return status, reply
		}
		return 404, `{"ok":false,"error":"unexpected"}`
	})
	t.Cleanup(d.close)
	return d
}

func progressRegistry(d *fakeDaemon, opts ...registryOption) *registry {
	opts = append([]registryOption{withAttentionToken("launch-tok")}, opts...)
	return newRegistry(newDaemonClient(d.port(), "agent-token"), "assistant", leotools.Permissions{}, opts...)
}

func TestLeoProgressReportsWithTheLaunchToken(t *testing.T) {
	d := progressDaemon(t, 200, progressReply)

	out, err := progressRegistry(d).call("leo_progress", json.RawMessage(
		`{"id":"build","title":"Xcode build","percent":62,"status":"Compiling","eta_seconds":90,"state":"running"}`))

	if err != nil {
		t.Fatal(err)
	}
	var task map[string]any
	if err := json.Unmarshal([]byte(out), &task); err != nil || task["id"] != "build" || task["percent"] != 62.0 {
		t.Fatalf("result = %s (%v)", out, err)
	}
	path, body := lastBody(t, d)
	want := map[string]any{"token": "launch-tok", "id": "build", "title": "Xcode build", "percent": 62.0, "status": "Compiling", "eta_seconds": 90.0, "state": "running"}
	if path != "/api/agent/progress" || len(body) != len(want) {
		t.Fatalf("call %s %v", path, body)
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%s] = %v, want %v", k, body[k], v)
		}
	}
}

// The tool has no agent argument: whatever the model passes cannot name
// another agent, and LEO_PROCESS_NAME never enters the request.
func TestLeoProgressNeverSendsAnAgentName(t *testing.T) {
	d := progressDaemon(t, 200, progressReply)

	if _, err := progressRegistry(d).call("leo_progress", json.RawMessage(`{"id":"build","title":"T","agent":"victim"}`)); err != nil {
		t.Fatal(err)
	}

	_, body := lastBody(t, d)
	for _, k := range []string{"agent", "name"} {
		if _, has := body[k]; has {
			t.Fatalf("body carries %q: %v", k, body)
		}
	}
	if len(body) != 3 { // token, id, title
		t.Fatalf("body = %v", body)
	}
}

func TestLeoProgressIndeterminate(t *testing.T) {
	d := progressDaemon(t, 200, progressReply)
	if _, err := progressRegistry(d).call("leo_progress", json.RawMessage(`{"id":"build","title":"T","indeterminate":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, body := lastBody(t, d); body["indeterminate"] != true {
		t.Fatalf("body = %v", body)
	}
}

func TestLeoProgressWithoutALaunchTokenExplainsWhy(t *testing.T) {
	d := progressDaemon(t, 200, progressReply)
	reg := newRegistry(newDaemonClient(d.port(), "agent-token"), "assistant", leotools.Permissions{})

	_, err := reg.call("leo_progress", json.RawMessage(`{"id":"build","title":"T"}`))

	if err == nil || !strings.Contains(err.Error(), "LEO_ATTENTION_TOKEN") {
		t.Fatalf("err = %v", err)
	}
	if len(d.calls) != 0 {
		t.Fatal("must not call the daemon without a token")
	}
}

func TestLeoProgressRefusesADispatchSubagent(t *testing.T) {
	d := progressDaemon(t, 200, progressReply)

	_, err := progressRegistry(d, withDispatchID("d-abc")).call("leo_progress", json.RawMessage(`{"id":"build","title":"T"}`))

	if err == nil || !strings.Contains(err.Error(), "dispatch") {
		t.Fatalf("err = %v", err)
	}
	if len(d.calls) != 0 {
		t.Fatal("a dispatch subagent must not reach the daemon")
	}
}

func TestLeoProgressSurfacesDaemonErrors(t *testing.T) {
	d := progressDaemon(t, 409, `{"ok":false,"error":"too many running tasks (max 8); finish one first"}`)

	_, err := progressRegistry(d).call("leo_progress", json.RawMessage(`{"id":"build","title":"T"}`))

	if err == nil || !strings.Contains(err.Error(), "too many running tasks") {
		t.Fatalf("err = %v", err)
	}
}

func TestLeoProgressValidatesArguments(t *testing.T) {
	d := progressDaemon(t, 200, progressReply)
	reg := progressRegistry(d)
	for name, args := range map[string]string{
		"missing id":      `{"title":"T"}`,
		"id not string":   `{"id":5}`,
		"percent string":  `{"id":"a","percent":"5"}`,
		"bad state":       `{"id":"a","state":"paused"}`,
		"eta negative":    `{"id":"a","eta_seconds":-1}`,
		"both modes":      `{"id":"a","percent":5,"indeterminate":true}`,
		"indeterminate s": `{"id":"a","indeterminate":"yes"}`,
	} {
		if _, err := reg.call("leo_progress", json.RawMessage(args)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if len(d.calls) != 0 {
		t.Fatal("invalid arguments must not reach the daemon")
	}
}

func TestLeoProgressIsDeniedByTemplatePermissions(t *testing.T) {
	d := progressDaemon(t, 200, progressReply)
	reg := newRegistry(newDaemonClient(d.port(), "agent-token"), "assistant",
		leotools.Permissions{DenyTools: []string{"leo_progress"}}, withAttentionToken("launch-tok"))
	for _, def := range reg.defs {
		if def.Name == "leo_progress" {
			t.Fatal("a denied tool must not be listed")
		}
	}
}
