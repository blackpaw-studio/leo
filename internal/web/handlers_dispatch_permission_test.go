package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDispatchPermissionHTTP(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/dispatch/d-nope/permission", strings.NewReader(`{`))
	req.SetPathValue("id", "d-nope")
	s.handleAPIDispatchPermission(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed: status %d: %s", w.Code, w.Body.String())
	}
	// An unknown run answers at once with no decision, so the hook falls
	// back to the pane's own prompt instead of hanging.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/dispatch/d-nope/permission", strings.NewReader(`{"payload":{"tool_name":"Bash","tool_input":{"command":"ls"}}}`))
	req.SetPathValue("id", "d-nope")
	s.handleAPIDispatchPermission(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"data":{}`) {
		t.Fatalf("unknown run: status %d: %s", w.Code, w.Body.String())
	}
}

func TestDispatchSendDecisionHTTP(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	call := func(body string) (int, string) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/dispatch/d-nope/send", strings.NewReader(body))
		req.SetPathValue("id", "d-nope")
		s.handleAPIDispatchSend(w, req)
		return w.Code, w.Body.String()
	}
	if code, body := call(`{"decision":"allow"}`); code != http.StatusConflict || !strings.Contains(body, "unknown dispatch") {
		t.Fatalf("decision on unknown run: %d %s", code, body)
	}
	if code, body := call(`{"message":"hi","decision":"allow"}`); code != http.StatusBadRequest || !strings.Contains(body, "either") {
		t.Fatalf("message+decision: %d %s", code, body)
	}
}
