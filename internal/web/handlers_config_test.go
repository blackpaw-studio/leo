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

// taskFormBase renders the named task's current config into a base
// url.Values set, using the exact same Kind-driven encoding as
// schema/values_test.go's private renderToForm helper (unreachable from this
// package since it's unexported). This lets a test override only the field(s)
// it cares about instead of hand-listing all 22 SectionTask fields — the
// GET-then-scrape approach the brief also floats is more brittle (it'd need
// to parse rendered HTML back into form values), so this is the cleaner of
// the two options it offers.
func taskFormBase(t *testing.T, s *Server, name string) url.Values {
	t.Helper()
	cfg, err := s.loadConfig()
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	task, ok := cfg.Tasks[name]
	if !ok {
		t.Fatalf("seed task %q not found", name)
	}
	form := url.Values{}
	for _, fv := range schema.Values(&task, schema.SectionTask, nil) {
		switch fv.Kind {
		case schema.KindBool:
			form.Add(fv.Key, "false")
			if fv.Checked {
				form.Add(fv.Key, "true")
			}
		default:
			form.Set(fv.Key, fv.Value)
		}
	}
	return form
}

// TestTaskSaveCoversNewFields guards handleConfigTaskSave against the same
// registry-drift risk Task 6 covered for defaults: runtime/session/lazy/
// queue_max are the fields added since the old hand-rolled handleConfigTask,
// and must round-trip through the schema-driven save path.
func TestTaskSaveCoversNewFields(t *testing.T) {
	s, dir := newTestServer(t) // seed config's "demo" task (web_test.go)
	form := taskFormBase(t, s, "demo")
	form.Set("runtime", "persistent")
	form.Set("session", "")
	form.Set("queue_max", "7")
	form.Set("lazy", "false")
	form.Add("lazy", "true")

	w := postForm(t, s, "/web/config/task/demo", form)
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d, body=%s", w.Code, readBody(t, w))
	}
	cfg := reloadTestConfig(t, dir)
	task := cfg.Tasks["demo"]
	if task.Runtime != "persistent" || task.QueueMax != 7 || !task.Lazy {
		t.Errorf("new fields not saved: %+v", task)
	}
}

// TestTaskAddRedirectsToEdit exercises the add-then-edit flow through the
// full server/middleware stack (handlers_task_prompt_test.go's
// TestHandleTaskAdd_RedirectsWithAutoPromptFile covers the same handler
// directly, without routing/middleware, so both are kept).
func TestTaskAddRedirectsToEdit(t *testing.T) {
	s, dir := newTestServer(t)
	form := url.Values{"name": {"fresh-task"}, "schedule": {"0 9 * * *"}}
	w := postForm(t, s, "/web/task/add", form)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/tasks/fresh-task" {
		t.Errorf("add: status=%d loc=%q", w.Code, w.Header().Get("Location"))
	}
	cfg := reloadTestConfig(t, dir)
	if _, ok := cfg.Tasks["fresh-task"]; !ok {
		t.Error("task not created")
	}
}

// TestTaskEditPageShowsAllFields is the "22 fields" self-review check: every
// TaskConfig field must render on /tasks/{name}, either in the primary form
// or inside the Advanced <details>.
func TestTaskEditPageShowsAllFields(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/tasks/demo", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()

	for _, key := range []string{
		"schedule", "timezone", "enabled", "prompt_file", "model", "provider",
		"max_turns", "timeout", "retries", "silent", "runtime", "session",
		"lazy", "queue_max", "channels", "dev_channels", "notify_on_fail",
		"permission_mode", "allowed_tools", "disallowed_tools",
		"append_system_prompt", "workspace",
	} {
		if !strings.Contains(body, `name="`+key+`"`) {
			t.Errorf("task edit page missing field %q", key)
		}
	}
}

// TestTaskEditPageNotFound guards the 404 branch of handleTaskEditPage: an
// unknown task name must not fall through to a 200 empty-form page.
func TestTaskEditPageNotFound(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/tasks/does-not-exist", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestTaskDeleteRedirectsToList guards the edit page's delete button: on
// success the handler must send HX-Redirect: /tasks so htmx navigates the
// browser back to the list (the edit page itself no longer has anything to
// show once the task is gone).
func TestTaskDeleteRedirectsToList(t *testing.T) {
	s, dir := newTestServer(t)

	req := httptest.NewRequest(http.MethodDelete, "/web/task/demo/delete", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, readBody(t, w))
	}
	if loc := w.Header().Get("HX-Redirect"); loc != "/tasks" {
		t.Errorf("HX-Redirect = %q, want /tasks", loc)
	}

	cfg := reloadTestConfig(t, dir)
	if _, ok := cfg.Tasks["demo"]; ok {
		t.Error("task should have been deleted")
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
