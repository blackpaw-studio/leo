package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDaemonClientSetsBearerAuth asserts the MCP daemon client always attaches
// the Authorization: Bearer header when constructed with a non-empty token.
// Both the apiEnvelope path (do) and the raw-response path (interrupt) go
// through setAuth, so we exercise both.
func TestDaemonClientSetsBearerAuth(t *testing.T) {
	const token = "token-under-test"

	var gotAuths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuths = append(gotAuths, r.Header.Get("Authorization"))
		// Respond successfully in both envelopes so the client is happy.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"data":{}}`))
	}))
	defer srv.Close()
	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")

	c := newDaemonClient(port, token)

	if _, err := c.listTasks(); err != nil {
		t.Fatalf("listTasks: %v", err)
	}
	if err := c.interrupt("proc"); err != nil {
		t.Fatalf("interrupt: %v", err)
	}

	want := "Bearer " + token
	if len(gotAuths) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(gotAuths))
	}
	for i, got := range gotAuths {
		if got != want {
			t.Errorf("request %d Authorization = %q, want %q", i, got, want)
		}
	}
}

// TestDaemonClientOmitsBearerWhenTokenEmpty ensures the client is safe to
// construct with no token — the server-side tests that spin up fake daemons
// rely on this.
func TestDaemonClientOmitsBearerWhenTokenEmpty(t *testing.T) {
	var gotAuths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuths = append(gotAuths, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"data":{}}`))
	}))
	defer srv.Close()
	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")

	c := newDaemonClient(port, "")
	if _, err := c.listTasks(); err != nil {
		t.Fatalf("listTasks: %v", err)
	}
	if err := c.interrupt("proc"); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if len(gotAuths) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(gotAuths))
	}
	for i, got := range gotAuths {
		if got != "" {
			t.Errorf("request %d Authorization = %q, want empty", i, got)
		}
	}
}
