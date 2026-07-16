package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
)

// errBoomRestart is a sentinel error for the failure-path restart test.
var errBoomRestart = errors.New("boom")

// TestRestartBanner_HiddenByDefault verifies neither restart banner is shown
// when no process-affecting config changes have been saved.
func TestRestartBanner_HiddenByDefault(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/partials/status", nil)
	w := httptest.NewRecorder()
	s.handlePartialStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "restart-banner") {
		t.Errorf("banner should be hidden when neither flag is set; body = %q", body)
	}
}

// TestRestartBanner_ServiceRestartShown verifies the service-restart banner
// renders with its own copy + button when serviceRestartNeeded=true, and
// that the agents-restart banner stays hidden.
func TestRestartBanner_ServiceRestartShown(t *testing.T) {
	s, _ := newTestServer(t)
	s.serviceRestartNeeded.Store(true)

	req := httptest.NewRequest(http.MethodGet, "/partials/status", nil)
	w := httptest.NewRecorder()
	s.handlePartialStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()

	for _, want := range []string{
		`class="restart-banner"`,
		`role="alert"`,
		"Service restart required",
		`hx-post="/web/service/restart"`,
		"Restart Now",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("banner missing %q; body = %q", want, body)
		}
	}
	if strings.Contains(body, "Restart agents") {
		t.Errorf("agents-restart banner should be hidden; body = %q", body)
	}
}

// TestRestartBanner_AgentsRestartShown verifies the agents-restart banner
// renders with its own copy + button when agentsRestartNeeded=true, and that
// the service-restart banner stays hidden.
func TestRestartBanner_AgentsRestartShown(t *testing.T) {
	s, _ := newTestServer(t)
	s.agentsRestartNeeded.Store(true)

	req := httptest.NewRequest(http.MethodGet, "/partials/status", nil)
	w := httptest.NewRecorder()
	s.handlePartialStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()

	for _, want := range []string{
		`class="restart-banner"`,
		`role="alert"`,
		"Config saved",
		`hx-post="/web/agents/restart"`,
		"Restart agents",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("banner missing %q; body = %q", want, body)
		}
	}
	if strings.Contains(body, "Service restart required") {
		t.Errorf("service-restart banner should be hidden; body = %q", body)
	}
}

// TestRestartBanner_BothShown verifies both banners can render simultaneously.
func TestRestartBanner_BothShown(t *testing.T) {
	s, _ := newTestServer(t)
	s.serviceRestartNeeded.Store(true)
	s.agentsRestartNeeded.Store(true)

	req := httptest.NewRequest(http.MethodGet, "/partials/status", nil)
	w := httptest.NewRecorder()
	s.handlePartialStatus(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "Service restart required") || !strings.Contains(body, "Config saved") {
		t.Errorf("expected both banners; body = %q", body)
	}
}

// TestDefaultsSave_SetsAgentsRestartFlag verifies saving Defaults flags the
// agents-restart banner, not the service-restart banner.
func TestDefaultsSave_SetsAgentsRestartFlag(t *testing.T) {
	s, _ := newTestServer(t)

	form := strings.NewReader("model=sonnet")
	req := httptest.NewRequest(http.MethodPost, "/web/config/defaults", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleConfigDefaultsSave(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !s.agentsRestartNeeded.Load() {
		t.Error("expected agentsRestartNeeded to be set after Defaults save")
	}
	if s.serviceRestartNeeded.Load() {
		t.Error("Defaults save must not set serviceRestartNeeded")
	}
}

// TestWebSave_SetsServiceRestartFlag verifies saving Web UI settings flags the
// service-restart banner, not the agents-restart banner.
func TestWebSave_SetsServiceRestartFlag(t *testing.T) {
	s, _ := newTestServer(t)

	form := strings.NewReader("enabled=true&port=9797&bind=127.0.0.1")
	req := httptest.NewRequest(http.MethodPost, "/web/config/web", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleConfigWebSave(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !s.serviceRestartNeeded.Load() {
		t.Error("expected serviceRestartNeeded to be set after Web save")
	}
	if s.agentsRestartNeeded.Load() {
		t.Error("Web save must not set agentsRestartNeeded")
	}
}

// TestTemplateSave_SetsAgentsRestartFlag verifies saving a Template flags the
// agents-restart banner (templates only affect future spawns immediately,
// but Restart can now re-apply a template change to a running agent).
func TestTemplateSave_SetsAgentsRestartFlag(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	form := strings.NewReader("model=sonnet")
	req := httptest.NewRequest(http.MethodPost, "/web/config/template/coding", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("name", "coding")
	w := httptest.NewRecorder()
	s.handleConfigTemplateSave(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !s.agentsRestartNeeded.Load() {
		t.Error("expected agentsRestartNeeded to be set after Template save")
	}
	if s.serviceRestartNeeded.Load() {
		t.Error("Template save must not set serviceRestartNeeded")
	}
}

// TestServiceRestart_ClearsOnlyServiceFlag verifies POST /web/service/restart
// clears serviceRestartNeeded but leaves agentsRestartNeeded untouched.
func TestServiceRestart_ClearsOnlyServiceFlag(t *testing.T) {
	s, _ := newTestServer(t)
	s.serviceRestartNeeded.Store(true)
	s.agentsRestartNeeded.Store(true)
	s.execCommand = func(name string, args ...string) *exec.Cmd { return exec.Command("true") }

	req := httptest.NewRequest(http.MethodPost, "/web/service/restart", nil)
	w := httptest.NewRecorder()
	s.handleServiceRestart(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if s.serviceRestartNeeded.Load() {
		t.Error("expected serviceRestartNeeded to be cleared")
	}
	if !s.agentsRestartNeeded.Load() {
		t.Error("service restart must not clear agentsRestartNeeded")
	}
}

// TestAgentsRestart_ClearsOnlyAgentsFlagOnSuccess verifies POST
// /web/agents/restart clears agentsRestartNeeded (even with skips) but
// leaves serviceRestartNeeded untouched.
func TestAgentsRestart_ClearsOnlyAgentsFlagOnSuccess(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	s.serviceRestartNeeded.Store(true)
	s.agentsRestartNeeded.Store(true)
	svc.restartAllResult = agent.RestartResult{
		Restarted: []string{"leo-a"},
		Skipped:   []string{"leo-b"},
		Failed:    map[string]error{},
	}

	req := httptest.NewRequest(http.MethodPost, "/web/agents/restart", nil)
	w := httptest.NewRecorder()
	s.handleAgentsRestart(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if !svc.restartAllCalled {
		t.Fatal("expected RestartAll to be called")
	}
	if s.agentsRestartNeeded.Load() {
		t.Error("expected agentsRestartNeeded to be cleared after a successful batch")
	}
	if !s.serviceRestartNeeded.Load() {
		t.Error("agents restart must not clear serviceRestartNeeded")
	}
	body := w.Body.String()
	if !strings.Contains(body, "Restarted 1 agent(s)") || !strings.Contains(body, "skipped 1") {
		t.Errorf("expected summary with restarted+skipped counts; body = %q", body)
	}
}

// TestAgentsRestart_KeepsFlagOnFailure verifies a batch with any failure
// leaves agentsRestartNeeded set and renders an error flash.
func TestAgentsRestart_KeepsFlagOnFailure(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	s.agentsRestartNeeded.Store(true)
	svc.restartAllResult = agent.RestartResult{
		Restarted: []string{"leo-a"},
		Failed:    map[string]error{"leo-b": errBoomRestart},
	}

	req := httptest.NewRequest(http.MethodPost, "/web/agents/restart", nil)
	w := httptest.NewRecorder()
	s.handleAgentsRestart(w, req)

	if !s.agentsRestartNeeded.Load() {
		t.Error("expected agentsRestartNeeded to stay set when a failure occurred")
	}
	body := w.Body.String()
	if !strings.Contains(body, "1 failed") {
		t.Errorf("expected failure count in flash; body = %q", body)
	}
}
