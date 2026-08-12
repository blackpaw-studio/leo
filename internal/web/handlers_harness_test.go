package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// seedHarnessTestServer spins up the shared test server (web_test.go's
// newTestServer) and immediately overwrites its on-disk config with cfg, so
// these handler tests can seed arbitrary Tasks/Templates/Defaults instead of
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
	cfg := &config.Config{Tasks: map[string]config.TaskConfig{"b": {
		Schedule:       "@daily",
		PromptFile:     "p.md",
		HarnessOptions: map[string]any{"permission_mode": "plan"},
	}}}
	s := seedHarnessTestServer(t, cfg)

	// Same harness as stored (claude via default) → stored values render.
	body := getBody(t, s, "/web/partials/harness-options?section=task&scope=b&harness=claude")
	if !strings.Contains(body, `name="harness_options.permission_mode"`) {
		t.Errorf("claude partial missing permission_mode field: %s", body)
	}
	if !strings.Contains(body, `value="plan" selected`) {
		t.Errorf("claude partial does not preselect stored value plan: %s", body)
	}
	if !strings.Contains(body, `hx-swap-oob`) || !strings.Contains(body, `dl-model-task-b`) {
		t.Errorf("partial missing OOB datalist refresh: %s", body)
	}

	// Different harness → blank slate for that harness's fields.
	body = getBody(t, s, "/web/partials/harness-options?section=task&scope=b&harness=codex")
	if !strings.Contains(body, `name="harness_options.permission_mode"`) {
		t.Errorf("codex partial missing permission_mode field: %s", body)
	}
	// claude and codex both expose a permission_mode key, so a bare
	// substring can no longer tell the two schemas apart. Assert instead on
	// a claude-only field, plus the blank-slate invariant scoped to the
	// shared key: enum <option>s are rendered from the selected harness's
	// schema, so a leaked claude value shows up not as a stray option but as
	// the empty option losing its `selected`.
	if strings.Contains(body, "harness_options.bypass_permissions") {
		t.Errorf("codex partial leaked claude fields: %s", body)
	}
	sel := regexp.MustCompile(`(?s)<select id="ho-permission_mode-task-b".*?</select>`).FindString(body)
	if sel == "" {
		t.Fatalf("codex partial has no permission_mode select: %s", body)
	}
	if !strings.Contains(sel, `value="" selected`) {
		t.Errorf("codex partial did not blank-slate permission_mode across the harness switch: %s", sel)
	}
}

func TestHarnessOptionsPartialRejectsUnknown(t *testing.T) {
	s := seedHarnessTestServer(t, &config.Config{})
	for _, path := range []string{
		"/web/partials/harness-options?section=task&scope=nope&harness=claude", // unknown scope
		"/web/partials/harness-options?section=bogus&scope=x&harness=claude",   // unknown section
		"/web/partials/harness-options?section=defaults&harness=doesnotexist",  // unknown harness
	} {
		if code := getStatus(t, s, path); code != http.StatusBadRequest && code != http.StatusNotFound {
			t.Errorf("%s → %d, want 400/404", path, code)
		}
	}
}

// TestHarnessOptionsPartialTemplateScope exercises locateHarnessScope's
// template branch, which the brief's core three tests (process/task/
// defaults) don't reach.
func TestHarnessOptionsPartialTemplateScope(t *testing.T) {
	cfg := &config.Config{
		Templates: map[string]config.TemplateConfig{"coding": {
			HarnessOptions: map[string]any{"permission_mode": "auto"},
		}},
	}
	s := seedHarnessTestServer(t, cfg)

	body := getBody(t, s, "/web/partials/harness-options?section=template&scope=coding&harness=claude")
	if !strings.Contains(body, `name="harness_options.permission_mode"`) {
		t.Errorf("template partial missing permission_mode field: %s", body)
	}
	if !strings.Contains(body, `dl-model-template-coding`) {
		t.Errorf("template partial missing scoped datalist id: %s", body)
	}
}

// hxGetPattern extracts the harness select's harness-options hx-get URL from
// a rendered page/form.html fragment (pages carry other unrelated hx-get
// polling endpoints too, e.g. /partials/status).
var hxGetPattern = regexp.MustCompile(`hx-get="([^"]*harness-options[^"]*)"`)

// TestHarnessSelectHxGetRoundTrips is the regression test for the bug fixed
// by scopeSuffix: form.html's harness-select hx-get URL must carry the RAW
// config map key ("b"), not the element-id suffix ("task-b"), because
// locateHarnessScope (and every cfg.Tasks[name]-style lookup behind it) keys
// on the raw name. It renders the REAL task edit page — the actual
// production wiring (handleTaskEditPage → buildFormWithHarness → form.html),
// not a hand-built formData — and fires the exact hx-get URL the page emits
// back through the test server, which is what the old hand-typed-query-string
// tests structurally missed (they'd have passed unchanged against the buggy
// code). Formerly rendered the process edit page before the Processes
// section was removed.
func TestHarnessSelectHxGetRoundTrips(t *testing.T) {
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{"b": {
			Schedule:       "@daily",
			PromptFile:     "p.md",
			HarnessOptions: map[string]any{"permission_mode": "plan"},
		}},
	}
	s := seedHarnessTestServer(t, cfg)

	body := getBody(t, s, "/tasks/b")

	m := hxGetPattern.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("harness select hx-get URL not found on task edit page: %s", body)
	}
	hxGetURL := m[1]

	u, err := url.Parse(hxGetURL)
	if err != nil {
		t.Fatalf("parsing hx-get URL %q: %v", hxGetURL, err)
	}
	if got := u.Query().Get("scope"); got != "b" {
		t.Errorf("hx-get scope param = %q, want raw config key %q", got, "b")
	}

	if !strings.Contains(body, `id="f-harness" name="harness"`) {
		// sanity-check we found the harness select itself, not some other
		// hx-get on the page.
		t.Fatalf("rendered page missing harness select: %s", body)
	}

	fullURL := hxGetURL + "&harness=claude"
	req := httptest.NewRequest(http.MethodGet, fullURL, nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET %s → %d, want 200 (body: %s)", fullURL, w.Code, w.Body.String())
	}
	respBody := w.Body.String()
	if !strings.Contains(respBody, `name="harness_options.permission_mode"`) {
		t.Errorf("partial response missing permission_mode field: %s", respBody)
	}
}

func TestHarnessOptionsPartialEmptyHarnessMeansInherit(t *testing.T) {
	cfg := &config.Config{Defaults: config.DefaultsConfig{Harness: "codex"},
		Tasks: map[string]config.TaskConfig{"n": {Schedule: "@daily", PromptFile: "p.md"}}}
	s := seedHarnessTestServer(t, cfg)
	body := getBody(t, s, "/web/partials/harness-options?section=task&scope=n&harness=")
	if !strings.Contains(body, `name="harness_options.permission_mode"`) {
		t.Errorf("inherit resolution failed — want codex fields, got: %s", body)
	}
}
