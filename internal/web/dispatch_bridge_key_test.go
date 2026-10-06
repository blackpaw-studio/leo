package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
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
