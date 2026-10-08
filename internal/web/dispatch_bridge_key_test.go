package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/bridge"
)

func startDispatch(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAPIDispatch(w, httptest.NewRequest("POST", "/api/dispatch", strings.NewReader(body)))
	return w
}

func TestAPIDispatchRecordsTheCallersBridgeKey(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	s.consults.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"done","is_error":false}`)
	}
	w := startDispatch(t, s, `{"from":"caller","template":"coding","prompt":"work","cwd":"/tmp","caller_bridge_key":"caller.ab12"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var started struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	rec, err := s.consults.Get(started.Data.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.CallerBridgeKey != "caller.ab12" {
		t.Fatalf("CallerBridgeKey = %q", rec.CallerBridgeKey)
	}
}

func TestAPIDispatchRejectsAnInvalidBridgeKey(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	w := startDispatch(t, s, `{"from":"caller","template":"coding","prompt":"work","cwd":"/tmp","caller_bridge_key":"../x"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
	}
}

// A dispatch records the launch its caller's key is open for, so it holds
// that launch's agent only: once the key is reopened for another launch
// (a recreated agent of the same name) the dispatch is nobody's.
func TestAPIDispatchRecordsTheCallersLaunchAndOwnsByIt(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t) // agent "leo-coding-leo"
	const name, key = "leo-coding-leo", "lcl-key"
	hub := bridge.New(bridge.Options{})
	t.Cleanup(hub.Close)
	if _, err := hub.Open(key, "launch-1"); err != nil {
		t.Fatal(err)
	}
	s.processes = &mockProcesses{states: map[string]ProcessStateInfo{name: {Name: name, Status: "running"}}}
	s.bridgeRouter = &bridge.Router{Hub: hub, Targets: func(agent string) (bridge.Target, bool) {
		return bridge.Target{Key: key}, agent == name
	}}
	s.consults.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "5")
	}
	w := startDispatch(t, s, `{"from":"caller","template":"coding","prompt":"work","cwd":"/tmp","caller_bridge_key":"lcl-key"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var started struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	rec, err := s.consults.Get(started.Data.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := bridge.LaunchID(key, "launch-1"); rec.CallerBridgeLaunch != want {
		t.Fatalf("CallerBridgeLaunch = %q; want %q", rec.CallerBridgeLaunch, want)
	}
	if owner, ok := s.dispatchCallerOwner(key, rec.CallerBridgeLaunch); !ok || owner != name {
		t.Fatalf("owner = %q, %v; want %s", owner, ok, name)
	}
	if _, err := hub.Open(key, "launch-2"); err != nil {
		t.Fatal(err)
	}
	if owner, ok := s.dispatchCallerOwner(key, rec.CallerBridgeLaunch); ok {
		t.Fatalf("owner = %q after the key was reopened for another launch; want none", owner)
	}
}

func TestAPIDispatchRecordsTheParentDispatchAndInheritsItsCaller(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	s.consults.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "5")
	}
	idOf := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("start: %d %s", w.Code, w.Body.String())
		}
		var started struct {
			Data struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil {
			t.Fatal(err)
		}
		return started.Data.ID
	}
	parent := idOf(startDispatch(t, s, `{"from":"root-agent","template":"coding","prompt":"work","cwd":"/tmp"}`))
	child := idOf(startDispatch(t, s, `{"from":"inherited","template":"coding","prompt":"work","cwd":"/tmp","parent_dispatch_id":"`+parent+`"}`))
	rec, err := s.consults.Get(child)
	if err != nil {
		t.Fatal(err)
	}
	if rec.ParentDispatchID != parent || rec.Caller != "root-agent" {
		t.Fatalf("parent=%q caller=%q, want %q/root-agent", rec.ParentDispatchID, rec.Caller, parent)
	}
}

func TestAPIDispatchRejectsAnInvalidParentDispatchID(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	for _, id := range []string{"../x", "a b", "d-1;rm"} {
		w := startDispatch(t, s, `{"from":"caller","template":"coding","prompt":"work","cwd":"/tmp","parent_dispatch_id":"`+id+`"}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("id %q: status %d, want 400: %s", id, w.Code, w.Body.String())
		}
	}
}
