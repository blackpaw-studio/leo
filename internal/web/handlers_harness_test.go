package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// seedHarnessTestServer spins up the shared test server (web_test.go's
// newTestServer) and immediately overwrites its on-disk config with cfg, so
// these handler tests can seed arbitrary Processes/Tasks/Defaults instead of
// the fixed testConfigYAML fixture — mirrors handlers_sessions_test.go's
// config.Save seeding pattern.
func seedHarnessTestServer(t *testing.T, cfg *config.Config) *Server {
	t.Helper()
	s, dir := newTestServer(t)
	if err := config.Save(filepath.Join(dir, "leo.yaml"), cfg); err != nil {
		t.Fatalf("seeding config: %v", err)
	}
	return s
}

// getBody issues an authenticated GET (via the server's already-wrapped
// handler, see newTestServer) and returns the response body.
func getBody(t *testing.T, s *Server, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	b, err := io.ReadAll(w.Result().Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return string(b)
}

// getStatus issues an authenticated GET and returns only the status code.
func getStatus(t *testing.T, s *Server, path string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	return w.Code
}

func TestHarnessOptionsPartialRendersSelectedHarness(t *testing.T) {
	cfg := &config.Config{Processes: map[string]config.ProcessConfig{"b": {
		HarnessOptions: map[string]any{"permission_mode": "plan"},
	}}}
	s := seedHarnessTestServer(t, cfg)

	// Same harness as stored (claude via default) → stored values render.
	body := getBody(t, s, "/web/partials/harness-options?section=process&scope=b&harness=claude")
	if !strings.Contains(body, `name="harness_options.permission_mode"`) {
		t.Errorf("claude partial missing permission_mode field: %s", body)
	}
	if !strings.Contains(body, `value="plan" selected`) {
		t.Errorf("claude partial does not preselect stored value plan: %s", body)
	}
	if !strings.Contains(body, `hx-swap-oob`) || !strings.Contains(body, `dl-model-process-b`) {
		t.Errorf("partial missing OOB datalist refresh: %s", body)
	}

	// Different harness → blank slate for that harness's fields.
	body = getBody(t, s, "/web/partials/harness-options?section=process&scope=b&harness=codex")
	if !strings.Contains(body, `name="harness_options.sandbox"`) {
		t.Errorf("codex partial missing sandbox field: %s", body)
	}
	if strings.Contains(body, "permission_mode") {
		t.Errorf("codex partial leaked claude fields: %s", body)
	}
}

func TestHarnessOptionsPartialRejectsUnknown(t *testing.T) {
	s := seedHarnessTestServer(t, &config.Config{})
	for _, path := range []string{
		"/web/partials/harness-options?section=process&scope=nope&harness=claude", // unknown scope
		"/web/partials/harness-options?section=bogus&scope=x&harness=claude",      // unknown section
		"/web/partials/harness-options?section=defaults&harness=doesnotexist",     // unknown harness
	} {
		if code := getStatus(t, s, path); code != http.StatusBadRequest && code != http.StatusNotFound {
			t.Errorf("%s → %d, want 400/404", path, code)
		}
	}
}

// TestHarnessOptionsPartialTemplateAndSessionScopes exercises
// locateHarnessScope's template and session branches, which the brief's core
// three tests (process/task/defaults) don't reach.
func TestHarnessOptionsPartialTemplateAndSessionScopes(t *testing.T) {
	cfg := &config.Config{
		Templates: map[string]config.TemplateConfig{"coding": {
			HarnessOptions: map[string]any{"permission_mode": "auto"},
		}},
		Sessions: map[string]config.SessionConfig{"r": {Workspace: "/w"}},
	}
	s := seedHarnessTestServer(t, cfg)

	body := getBody(t, s, "/web/partials/harness-options?section=template&scope=coding&harness=claude")
	if !strings.Contains(body, `name="harness_options.permission_mode"`) {
		t.Errorf("template partial missing permission_mode field: %s", body)
	}
	if !strings.Contains(body, `dl-model-template-coding`) {
		t.Errorf("template partial missing scoped datalist id: %s", body)
	}

	body = getBody(t, s, "/web/partials/harness-options?section=session&scope=r&harness=claude")
	if !strings.Contains(body, `dl-model-session-r`) {
		t.Errorf("session partial missing scoped datalist id: %s", body)
	}
}

func TestHarnessOptionsPartialEmptyHarnessMeansInherit(t *testing.T) {
	cfg := &config.Config{Defaults: config.DefaultsConfig{Harness: "codex"},
		Tasks: map[string]config.TaskConfig{"n": {Schedule: "@daily", PromptFile: "p.md"}}}
	s := seedHarnessTestServer(t, cfg)
	body := getBody(t, s, "/web/partials/harness-options?section=task&scope=n&harness=")
	if !strings.Contains(body, `name="harness_options.sandbox"`) {
		t.Errorf("inherit resolution failed — want codex fields, got: %s", body)
	}
}
