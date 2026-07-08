package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/web/schema"
)

// postForm posts a url-encoded form to path through the server's full,
// authenticated handler chain (newTestServer's bearer-token wrapper handles
// Host/Origin + session auth) and returns the recorded response.
func postForm(t *testing.T, s *Server, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	return w
}

// readBody drains a response recorder's body as a string.
func readBody(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	b, err := io.ReadAll(w.Result().Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return string(b)
}

// reloadTestConfig reads dir/leo.yaml back off disk, mirroring what a
// running daemon would see after a save.
func reloadTestConfig(t *testing.T, dir string) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join(dir, "leo.yaml"))
	if err != nil {
		t.Fatalf("reloading config: %v", err)
	}
	return cfg
}

func TestDefaultsSaveRoundTrip(t *testing.T) {
	s, dir := newTestServer(t)
	form := url.Values{}
	form.Set("model", "opus")
	form.Set("max_turns", "50")
	form.Set("permission_mode", "acceptEdits")
	form.Set("provider", "")
	form.Set("stale_resume_hours", "12")
	// KindBool pattern: hidden false + optional true.
	form.Add("bypass_permissions", "false")
	form.Add("remote_control", "false")
	w := postForm(t, s, "/web/config/defaults", form)
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d", w.Code)
	}
	cfg := reloadTestConfig(t, dir)
	if cfg.Defaults.Model != "opus" || cfg.Defaults.MaxTurns != 50 ||
		cfg.Defaults.StaleResumeHours != 12 {
		t.Errorf("saved defaults wrong: %+v", cfg.Defaults)
	}
}

func TestDefaultsSaveRejectsBadModel(t *testing.T) {
	s, dir := newTestServer(t)
	form := url.Values{}
	form.Set("model", "gpt-9000")
	w := postForm(t, s, "/web/config/defaults", form)
	body := readBody(t, w)
	if !strings.Contains(body, "flash-error") {
		t.Errorf("want validation flash, got: %s", body)
	}
	cfg := reloadTestConfig(t, dir)
	if cfg.Defaults.Model == "gpt-9000" {
		t.Error("invalid model was persisted")
	}
}

// TestPageConfigDefaultsShowsAllFields is the "11 fields" self-review check:
// every DefaultsConfig field must render on /config/defaults, either in the
// primary form or inside the Advanced <details>.
func TestPageConfigDefaultsShowsAllFields(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/config/defaults", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()

	for _, key := range []string{
		"model", "provider", "max_turns", "permission_mode",
		"bypass_permissions", "allowed_tools", "disallowed_tools",
		"remote_control", "append_system_prompt", "stale_resume_hours",
		"idle_suspend_after",
	} {
		if !strings.Contains(body, `name="`+key+`"`) {
			t.Errorf("defaults page missing field %q", key)
		}
	}
}

// TestConfigFormRendersEverySection is a structural smoke test: it builds a
// form for every schema.Section against a zero-value target and renders
// config_form, exercising every Kind branch (bool, tribool, select, envmap,
// textarea, cron, number, csv/duration/text-default) the field template
// supports, even though only SectionDefaults is wired to a live page in this
// task.
func TestConfigFormRendersEverySection(t *testing.T) {
	s, _ := newTestServer(t)
	cfg, err := s.loadConfig()
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}

	for _, section := range schema.AllSections() {
		target := reflect.New(schema.StructFor(section)).Interface()
		fd := s.buildForm(section, target, cfg, "/web/test")
		var buf strings.Builder
		if err := s.templates.ExecuteTemplate(&buf, "config_form", fd); err != nil {
			t.Errorf("section %s: rendering config_form: %v", section, err)
		}
	}
}
