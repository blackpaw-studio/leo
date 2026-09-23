package cli

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type capturedReport struct {
	path, auth string
	body       []byte
}

// reportServer records every request the report command makes.
func reportServer(t *testing.T) (port string, got func() []capturedReport) {
	t.Helper()
	var mu sync.Mutex
	var reqs []capturedReport
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, capturedReport{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: body})
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	_, p, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	return p, func() []capturedReport {
		mu.Lock()
		defer mu.Unlock()
		return append([]capturedReport(nil), reqs...)
	}
}

func runReport(t *testing.T, payload string) error {
	t.Helper()
	cmd := newDispatchReportCmd()
	cmd.SetIn(strings.NewReader(payload))
	cmd.SetArgs(nil)
	return cmd.Execute()
}

func clearReportEnv(t *testing.T) {
	for _, k := range []string{"LEO_DISPATCH_ID", "LEO_CONFIG", "LEO_ATTENTION_AGENT", "LEO_ATTENTION_TOKEN", "LEO_WEB_PORT", "LEO_API_TOKEN"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func TestDispatchReportRoutesAttentionHook(t *testing.T) {
	clearReportEnv(t)
	port, got := reportServer(t)
	t.Setenv("LEO_ATTENTION_TOKEN", "launch-tok")
	t.Setenv("LEO_WEB_PORT", port)
	t.Setenv("LEO_API_TOKEN", "agent-token")

	if err := runReport(t, `{"hook_event_name":"Stop","session_id":"s1"}`); err != nil {
		t.Fatalf("report: %v", err)
	}

	reqs := got()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	if reqs[0].path != "/api/agent/hook" || reqs[0].auth != "Bearer agent-token" {
		t.Fatalf("request = %+v", reqs[0])
	}
	var wrapped struct {
		Token   string          `json:"token"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(reqs[0].body, &wrapped); err != nil || wrapped.Token != "launch-tok" || string(wrapped.Payload) != `{"hook_event_name":"Stop","session_id":"s1"}` {
		t.Fatalf("body = %s (%v), want the launch token + raw hook payload", reqs[0].body, err)
	}
}

func TestDispatchReportIgnoresLegacyAgentName(t *testing.T) {
	clearReportEnv(t)
	port, got := reportServer(t)
	t.Setenv("LEO_ATTENTION_AGENT", "leo-worker")
	t.Setenv("LEO_WEB_PORT", port)

	if err := runReport(t, `{"hook_event_name":"Stop"}`); err != nil {
		t.Fatalf("report: %v", err)
	}
	if n := len(got()); n != 0 {
		t.Fatalf("requests = %d, want 0 (name routing is gone)", n)
	}
}

func TestDispatchReportPrefersDispatchRoute(t *testing.T) {
	clearReportEnv(t)
	port, got := reportServer(t)
	cfgPath := filepath.Join(t.TempDir(), "leo.yaml")
	portNum, _ := strconv.Atoi(port)
	if err := os.WriteFile(cfgPath, []byte("web:\n  port: "+strconv.Itoa(portNum)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LEO_DISPATCH_ID", "d-123")
	t.Setenv("LEO_CONFIG", cfgPath)
	t.Setenv("LEO_ATTENTION_TOKEN", "launch-tok")
	t.Setenv("LEO_WEB_PORT", port)

	if err := runReport(t, `{"hook_event_name":"Stop"}`); err != nil {
		t.Fatalf("report: %v", err)
	}

	reqs := got()
	if len(reqs) != 1 || reqs[0].path != "/api/dispatch/d-123/report" {
		t.Fatalf("requests = %+v, want one dispatch report", reqs)
	}
	var wrapped struct {
		EventID string          `json:"event_id"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(reqs[0].body, &wrapped); err != nil || wrapped.EventID == "" || string(wrapped.Payload) != `{"hook_event_name":"Stop"}` {
		t.Fatalf("dispatch body = %s (%v)", reqs[0].body, err)
	}
}

func TestDispatchReportNoOpWithoutRouteEnv(t *testing.T) {
	clearReportEnv(t)
	port, got := reportServer(t)
	t.Setenv("LEO_WEB_PORT", port)

	if err := runReport(t, `{"hook_event_name":"Stop"}`); err != nil {
		t.Fatalf("report: %v", err)
	}
	if n := len(got()); n != 0 {
		t.Fatalf("requests = %d, want 0", n)
	}
}

func TestDispatchReportAttentionFailureDoesNotFailHook(t *testing.T) {
	clearReportEnv(t)
	t.Setenv("LEO_ATTENTION_TOKEN", "launch-tok")
	t.Setenv("LEO_WEB_PORT", "1") // nothing listens on port 1

	if err := runReport(t, `{"hook_event_name":"Stop"}`); err != nil {
		t.Fatalf("report: %v, want nil (attention is best-effort)", err)
	}
}
