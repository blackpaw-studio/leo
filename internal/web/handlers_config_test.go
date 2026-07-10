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
	form.Set("stale_resume_hours", "12")
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

// TestPageConfigDefaultsShowsAllFields is the "10 fields" self-review check:
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
		"model", "max_turns", "stale_resume_hours",
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

// processFormBase renders the named process's current config into a base
// url.Values set, using the same Kind-driven encoding as taskFormBase above.
// Lets a test override only the field(s) it cares about instead of
// hand-listing all 18 SectionProcess fields.
func processFormBase(t *testing.T, s *Server, name string) url.Values {
	t.Helper()
	cfg, err := s.loadConfig()
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	proc, ok := cfg.Processes[name]
	if !ok {
		t.Fatalf("seed process %q not found", name)
	}
	form := url.Values{}
	for _, fv := range schema.Values(&proc, schema.SectionProcess, nil) {
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

// templateFormBase renders the named template's current config into a base
// url.Values set, using the same Kind-driven encoding as taskFormBase/
// processFormBase above. Lets a test override only the field(s) it cares
// about instead of hand-listing all 16 SectionTemplate fields.
func templateFormBase(t *testing.T, s *Server, name string) url.Values {
	t.Helper()
	cfg, err := s.loadConfig()
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	tmpl, ok := cfg.Templates[name]
	if !ok {
		t.Fatalf("seed template %q not found", name)
	}
	form := url.Values{}
	for _, fv := range schema.Values(&tmpl, schema.SectionTemplate, nil) {
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

// bypass_permissions/permission_mode's tri-state web-form coverage
// (TestProcessBypassTriState) was removed along with the field's web-form
// registration — see internal/web/schema/registry.go's Excluded map (Task 7:
// claude flat fields moved to harness_options). The claude options editor
// arrives with harness_options forms in a later plan.

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

// TestValidEntityName is a table test for the shared entity-name guard used
// by every add handler (task/process/template/host/session). A
// name flowing unchecked into a URL path segment or (for tasks) a
// prompts/<name>.md file path lets "/", "#", "?", or ".." create an
// unroutable config entry or escape the workspace.
func TestValidEntityName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"demo", true},
		{"demo-task", true},
		{"demo_task", true},
		{"demo.task", true},
		{"Demo123", true},
		{"", false},
		{".", false},
		{"..", false},
		{"a/b", false},
		{"../escape", false},
		{"a#b", false},
		{"a?b", false},
		{"a b", false},
		{"a\tb", false},
		{"a\\b", false},
	}
	for _, tt := range tests {
		if got := validEntityName(tt.name); got != tt.want {
			t.Errorf("validEntityName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestTaskAddRejectsInvalidName covers the redirect-shaped add handlers
// (task/process/template all redirect to an edit page on success): a name
// containing "/" must be rejected with a flash error before it ever reaches
// PromptFile: "prompts/" + name + ".md" in handleTaskAdd, which would
// otherwise let "a/../../etc" style names escape the workspace.
func TestTaskAddRejectsInvalidName(t *testing.T) {
	s, dir := newTestServer(t)
	form := url.Values{"name": {"a/b"}, "schedule": {"0 9 * * *"}}
	w := postForm(t, s, "/web/task/add", form)
	body := readBody(t, w)
	if !strings.Contains(body, "flash-error") {
		t.Errorf("want validation flash for invalid name, got status=%d body=%s", w.Code, body)
	}
	cfg := reloadTestConfig(t, dir)
	if _, ok := cfg.Tasks["a/b"]; ok {
		t.Error("invalid task name should not have been persisted")
	}
	if len(cfg.Tasks) != 3 { // heartbeat, cleanup, demo from testConfigYAML — unchanged
		t.Errorf("invalid add should not have touched config, got %d tasks: %+v", len(cfg.Tasks), cfg.Tasks)
	}
}

// TestTaskEditPageShowsAllFields is the "21 fields" self-review check: every
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
		"schedule", "timezone", "enabled", "prompt_file", "model",
		"max_turns", "timeout", "retries", "silent", "runtime", "session",
		"lazy", "queue_max", "channels", "dev_channels", "notify_on_fail",
		"workspace",
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

// TestProcessEditPageShowsAllFields is the "17 fields" self-review check:
// every ProcessConfig field must render on /processes/{name}, either in the
// primary form or inside the Advanced <details>.
func TestProcessEditPageShowsAllFields(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/processes/assistant", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()

	for _, key := range []string{
		"enabled", "workspace", "model", "max_turns",
		"channels", "dev_channels", "mcp_config", "add_dirs", "env",
		"stale_resume_hours",
	} {
		if !strings.Contains(body, `name="`+key+`"`) {
			t.Errorf("process edit page missing field %q", key)
		}
	}
}

// TestProcessEditPageNotFound guards the 404 branch of handleProcessEditPage:
// an unknown process name must not fall through to a 200 empty-form page.
func TestProcessEditPageNotFound(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/processes/does-not-exist", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestProcessAddRedirectsToEdit exercises the add-then-edit flow: adding a
// process creates a bare (disabled, name-only) entry and 303s straight to its
// edit page, mirroring TestTaskAddRedirectsToEdit.
func TestProcessAddRedirectsToEdit(t *testing.T) {
	s, dir := newTestServer(t)
	form := url.Values{"name": {"fresh-process"}}
	w := postForm(t, s, "/web/process/add", form)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/processes/fresh-process" {
		t.Errorf("add: status=%d loc=%q", w.Code, w.Header().Get("Location"))
	}
	cfg := reloadTestConfig(t, dir)
	proc, ok := cfg.Processes["fresh-process"]
	if !ok {
		t.Fatal("process not created")
	}
	if proc.Enabled {
		t.Error("new process should start disabled")
	}
}

// TestProcessDeleteRedirectsToList guards the edit page's delete button: on
// success the handler must send HX-Redirect: /processes so htmx navigates
// the browser back to the list.
func TestProcessDeleteRedirectsToList(t *testing.T) {
	s, dir := newTestServer(t)

	req := httptest.NewRequest(http.MethodDelete, "/web/process/assistant", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, readBody(t, w))
	}
	if loc := w.Header().Get("HX-Redirect"); loc != "/processes" {
		t.Errorf("HX-Redirect = %q, want /processes", loc)
	}

	cfg := reloadTestConfig(t, dir)
	if _, ok := cfg.Processes["assistant"]; ok {
		t.Error("process should have been deleted")
	}
}

// TestTemplateSaveNewFields guards handleConfigTemplateSave against the same
// registry-drift risk Task 6/8 covered for defaults/processes: idle_suspend_after
// was entirely missing from the old hand-rolled handleConfigTemplate, and must
// round-trip through the schema-driven save path.
func TestTemplateSaveNewFields(t *testing.T) {
	s, dir, _ := newTestServerWithAgents(t) // seed config's "coding" template (handlers_agents_test.go)
	form := templateFormBase(t, s, "coding")
	form.Set("idle_suspend_after", "2h")

	w := postForm(t, s, "/web/config/template/coding", form)
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d, body=%s", w.Code, readBody(t, w))
	}
	cfg := reloadTestConfig(t, dir)
	tmpl := cfg.Templates["coding"]
	if tmpl.IdleSuspendAfter != "2h" {
		t.Errorf("idle_suspend_after not saved: %+v", tmpl)
	}
}

// TestTemplateEditPageShowsAllFields is the "15 fields" self-review check:
// every TemplateConfig field must render on /config/templates/{name}, either
// in the primary form or inside the Advanced <details>.
func TestTemplateEditPageShowsAllFields(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	req := httptest.NewRequest(http.MethodGet, "/config/templates/coding", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()

	for _, key := range []string{
		"workspace", "model", "max_turns",
		"channels", "dev_channels", "mcp_config", "add_dirs", "env",
		"idle_suspend_after",
	} {
		if !strings.Contains(body, `name="`+key+`"`) {
			t.Errorf("template edit page missing field %q", key)
		}
	}
}

// TestTemplateEditPageNotFound guards the 404 branch of handleTemplateEditPage:
// an unknown template name must not fall through to a 200 empty-form page.
func TestTemplateEditPageNotFound(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	req := httptest.NewRequest(http.MethodGet, "/config/templates/does-not-exist", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// TestTaskEditPageDeleteButtonNotNestedForm is a regression test for the
// nested-<form> bug: config_form used to render the Delete control as a
// second <form hx-delete=...> nested inside the outer <form class=
// "config-form" hx-post=...>. HTML5 parsing ignores a form start tag while
// another form is already open, so the browser silently reassigned Delete's
// button to the OUTER form — clicking Delete actually fired the Save POST.
// Assert at the string level (cheapest way to pin real browser-parsing
// behavior without a headless browser in this test suite) that the outer
// form's body contains no nested "<form" and that hx-delete now lives on a
// <button>, not a <form>.
func TestTaskEditPageDeleteButtonNotNestedForm(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/tasks/demo", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()

	start := strings.Index(body, `<form class="config-form"`)
	if start == -1 {
		t.Fatal("outer config-form not found in task edit page")
	}
	end := strings.Index(body[start:], "</form>")
	if end == -1 {
		t.Fatal("outer config-form has no closing </form> tag")
	}
	outerForm := body[start : start+end]

	if strings.Contains(outerForm, "<form") == false {
		t.Fatal("sanity check failed: outer form body should contain its own opening tag substring")
	}
	// The outer opening tag itself contains "<form"; anything past its first
	// occurrence must not contain another one.
	afterOpenTag := outerForm[strings.Index(outerForm, ">")+1:]
	if strings.Contains(afterOpenTag, "<form") {
		t.Errorf("outer config-form still contains a nested <form>, want the Delete control to be a plain <button>:\n%s", outerForm)
	}
	if !strings.Contains(outerForm, "hx-delete=") {
		t.Fatal("expected hx-delete on the Delete control inside the outer form")
	}

	deleteIdx := strings.Index(outerForm, "hx-delete=")
	tagStart := strings.LastIndex(outerForm[:deleteIdx], "<")
	tag := outerForm[tagStart:]
	tagName := strings.TrimPrefix(tag[:strings.IndexAny(tag, " \t\n>")], "<")
	if tagName != "button" {
		t.Errorf("hx-delete is on a <%s>, want <button>", tagName)
	}
}

// deleteRequest sends a DELETE through the full server/middleware stack,
// mirroring postForm's helper role for POST requests.
func deleteRequest(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	return w
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

// TestWebConfigSave is this task's TDD anchor (adapted from the brief's
// sketch to this codebase's actual helpers — postForm/reloadTestConfig
// instead of postFormWithCookie, and newTestServer's dir return instead of
// a separate reload call). It exercises the KindBool hidden-false +
// optional-true encoding, KindNumber, and KindCSV round trip together.
func TestWebConfigSave(t *testing.T) {
	s, dir := newTestServer(t)
	form := url.Values{"port": {"8371"}, "bind": {"0.0.0.0"}, "allowed_hosts": {"10.0.4.16, 10.0.2.10"}}
	form.Add("enabled", "false")
	form.Add("enabled", "true")
	w := postForm(t, s, "/web/config/web", form)
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d, body=%s", w.Code, readBody(t, w))
	}
	cfg := reloadTestConfig(t, dir)
	if cfg.Web.Port != 8371 || cfg.Web.Bind != "0.0.0.0" || !cfg.Web.Enabled ||
		len(cfg.Web.AllowedHosts) != 2 {
		t.Errorf("web config not saved: %+v", cfg.Web)
	}
}

// TestWebConfigSaveRejectsBadPort pins the validation-refusal path through
// applySection: an out-of-range port must not be written to disk.
func TestWebConfigSaveRejectsBadPort(t *testing.T) {
	s, dir := newTestServer(t)
	form := url.Values{"port": {"70000"}, "bind": {""}, "allowed_hosts": {""}}
	form.Add("enabled", "false")
	w := postForm(t, s, "/web/config/web", form)
	body := readBody(t, w)
	if !strings.Contains(body, "flash-error") {
		t.Errorf("want validation flash for out-of-range port, got: %s", body)
	}
	cfg := reloadTestConfig(t, dir)
	if cfg.Web.Port == 70000 {
		t.Error("invalid port was persisted")
	}
}

// TestClientDefaultHostSave covers the Remote client card, which registers
// only default_host — hosts is excluded from SectionClient (see
// schema.Excluded) and gets its own map-CRUD handlers below.
func TestClientDefaultHostSave(t *testing.T) {
	s, dir := newTestServer(t)
	w := postForm(t, s, "/web/config/client", url.Values{"default_host": {"prod"}})
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d, body=%s", w.Code, readBody(t, w))
	}
	cfg := reloadTestConfig(t, dir)
	if cfg.Client.DefaultHost != "prod" {
		t.Errorf("default_host not saved: %+v", cfg.Client)
	}
}

// TestHostCRUD exercises add/edit/delete for cfg.Client.Hosts. HostConfig
// has no fields Config.Validate() requires non-empty, so add creates a
// genuinely empty entry (see handleHostAdd's doc comment) rather than a
// forced placeholder.
func TestHostCRUD(t *testing.T) {
	s, dir := newTestServer(t)

	// add
	w := postForm(t, s, "/web/host/add", url.Values{"name": {"prod"}})
	if w.Code != http.StatusOK {
		t.Fatalf("add: %d, body=%s", w.Code, readBody(t, w))
	}
	if w.Header().Get("HX-Refresh") != "true" {
		t.Errorf("add: HX-Refresh header = %q, want \"true\"", w.Header().Get("HX-Refresh"))
	}
	cfg := reloadTestConfig(t, dir)
	if _, ok := cfg.Client.Hosts["prod"]; !ok {
		t.Fatal("host not created")
	}

	// edit
	form := url.Values{
		"ssh":       {"alice@leo.example.com"},
		"ssh_args":  {"-p, 2222"},
		"leo_path":  {"/opt/leo/bin/leo"},
		"tmux_path": {"/opt/homebrew/bin/tmux"},
	}
	w = postForm(t, s, "/web/config/host/prod", form)
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d, body=%s", w.Code, readBody(t, w))
	}
	cfg = reloadTestConfig(t, dir)
	got := cfg.Client.Hosts["prod"]
	if got.SSH != "alice@leo.example.com" || got.LeoPath != "/opt/leo/bin/leo" ||
		got.TmuxPath != "/opt/homebrew/bin/tmux" || len(got.SSHArgs) != 2 {
		t.Errorf("host not saved: %+v", got)
	}

	// delete
	w = deleteRequest(t, s, "/web/host/prod")
	if w.Code != http.StatusOK {
		t.Fatalf("delete: %d, body=%s", w.Code, readBody(t, w))
	}
	if w.Header().Get("HX-Refresh") != "true" {
		t.Errorf("delete: HX-Refresh header = %q, want \"true\"", w.Header().Get("HX-Refresh"))
	}
	cfg = reloadTestConfig(t, dir)
	if _, ok := cfg.Client.Hosts["prod"]; ok {
		t.Error("host not deleted")
	}
}

// TestHostAddRejectsDuplicate guards handleHostAdd's existence check.
func TestHostAddRejectsDuplicate(t *testing.T) {
	s, dir := newTestServer(t)
	postForm(t, s, "/web/host/add", url.Values{"name": {"prod"}})

	w := postForm(t, s, "/web/host/add", url.Values{"name": {"prod"}})
	body := readBody(t, w)
	if !strings.Contains(body, "flash-error") {
		t.Errorf("want validation flash for duplicate name, got: %s", body)
	}
	cfg := reloadTestConfig(t, dir)
	if len(cfg.Client.Hosts) != 1 {
		t.Errorf("duplicate add should not have touched config: %+v", cfg.Client.Hosts)
	}
}

// TestHostAddRejectsEmptyName guards handleHostAdd's required-field check.
func TestHostAddRejectsEmptyName(t *testing.T) {
	s, dir := newTestServer(t)
	w := postForm(t, s, "/web/host/add", url.Values{"name": {""}})
	body := readBody(t, w)
	if !strings.Contains(body, "flash-error") {
		t.Errorf("want validation flash for empty name, got: %s", body)
	}
	cfg := reloadTestConfig(t, dir)
	if len(cfg.Client.Hosts) != 0 {
		t.Errorf("empty-name add should not have created a host: %+v", cfg.Client.Hosts)
	}
}

// TestHostDeleteNotFound guards the not-found branch of handleHostDelete.
func TestHostDeleteNotFound(t *testing.T) {
	s, _ := newTestServer(t)
	w := deleteRequest(t, s, "/web/host/does-not-exist")
	body := readBody(t, w)
	if !strings.Contains(body, "flash-error") {
		t.Errorf("want not-found flash, got: %s", body)
	}
}

// TestHostDeleteNoReferenceCheck pins the "no reference-check on delete"
// decision: deleting a host that's named by client.default_host must still
// succeed — nothing in Config.Validate() treats default_host as a foreign
// key into hosts.
func TestHostDeleteNoReferenceCheck(t *testing.T) {
	s, dir := newTestServer(t)
	postForm(t, s, "/web/host/add", url.Values{"name": {"prod"}})
	postForm(t, s, "/web/config/client", url.Values{"default_host": {"prod"}})

	w := deleteRequest(t, s, "/web/host/prod")
	if w.Code != http.StatusOK {
		t.Fatalf("delete: %d, body=%s", w.Code, readBody(t, w))
	}
	cfg := reloadTestConfig(t, dir)
	if _, ok := cfg.Client.Hosts["prod"]; ok {
		t.Error("host should have been deleted despite being default_host")
	}
	if cfg.Client.DefaultHost != "prod" {
		t.Errorf("default_host should be left untouched: %+v", cfg.Client)
	}
}

// TestPageConfigSettingsShowsWebFormAndWarnings guards the Web UI card: the
// port/bind/allowed_hosts lockout Warning strings from the schema registry
// must render on the page (config_field's fwarn), and this is the fix for
// the old read-only table's wrong 0.0.0.0-looks-live bind display — the
// real default now only ever shows via the Help placeholder/inherited text.
func TestPageConfigSettingsShowsWebFormAndWarnings(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/config/settings", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()

	for _, key := range []string{"enabled", "port", "bind", "allowed_hosts", "default_host"} {
		if !strings.Contains(body, `name="`+key+`"`) {
			t.Errorf("settings page missing field %q", key)
		}
	}
	if !strings.Contains(body, "Changing port or bind can lock you out of this UI") {
		t.Error("settings page missing port/bind lockout warning")
	}
	if !strings.Contains(body, "Removing your own address here will block your browser") {
		t.Error("settings page missing allowed_hosts lockout warning")
	}
	if !strings.Contains(body, "No remote hosts configured.") {
		t.Errorf("empty hosts state copy missing: %s", body)
	}
	if !strings.Contains(body, `hx-post="/web/host/add"`) {
		t.Error("settings page missing add-host form")
	}
	if !strings.Contains(body, `hx-post="/web/config/reload"`) {
		t.Error("settings page missing reload-config button")
	}
}

// TestPageConfigSettingsListsHostCards guards the populated-state host card
// list: each host gets its own inline config_form (Action
// /web/config/host/{name}, DeleteURL /web/host/{name}).
func TestPageConfigSettingsListsHostCards(t *testing.T) {
	s, _ := newTestServer(t)
	postForm(t, s, "/web/host/add", url.Values{"name": {"prod"}})

	req := httptest.NewRequest(http.MethodGet, "/config/settings", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, ">prod<") {
		t.Errorf("host name not rendered: %s", body)
	}
	if !strings.Contains(body, `hx-post="/web/config/host/prod"`) {
		t.Errorf("host card missing inline save form: %s", body)
	}
	if !strings.Contains(body, `hx-delete="/web/host/prod"`) {
		t.Errorf("host card missing delete action: %s", body)
	}
	for _, key := range []string{"ssh", "ssh_args", "leo_path", "tmux_path"} {
		if !strings.Contains(body, `name="`+key+`"`) {
			t.Errorf("host card missing field %q", key)
		}
	}
}
