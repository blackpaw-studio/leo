package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/web/schema"

	_ "github.com/blackpaw-studio/leo/internal/harness/codex"
	_ "github.com/blackpaw-studio/leo/internal/harness/opencode"
)

// apiHarness drives the JSON config routes through a mux, the way both the
// daemon socket and /api/v1 mount them.
type apiHarness struct {
	t    *testing.T
	svc  *ConfigService
	path string
	mux  *http.ServeMux
	pfx  string
}

func newAPIHarness(t *testing.T) *apiHarness {
	t.Helper()
	svc, path := newConfigService(t, nil)
	return newAPIHarnessOver(t, svc, path, "")
}

func newAPIHarnessOver(t *testing.T, svc *ConfigService, path, prefix string) *apiHarness {
	t.Helper()
	mux := http.NewServeMux()
	svc.RegisterConfigRoutes(mux, prefix, func(h http.HandlerFunc) http.HandlerFunc { return h })
	return &apiHarness{t: t, svc: svc, path: path, mux: mux, pfx: prefix}
}

type apiResult struct {
	Status int
	Header http.Header
	Raw    []byte
	Body   map[string]any
}

// data is the envelope's data object.
func (r apiResult) data() map[string]any {
	d, _ := r.Body["data"].(map[string]any)
	return d
}

func (r apiResult) errorFields() []map[string]any {
	var out []map[string]any
	list, _ := r.Body["errors"].([]any)
	for _, e := range list {
		out = append(out, e.(map[string]any))
	}
	return out
}

func (h *apiHarness) do(method, path, ifMatch string, body any) apiResult {
	h.t.Helper()
	var rdr *bytes.Reader
	switch b := body.(type) {
	case nil:
		rdr = bytes.NewReader(nil)
	case string:
		rdr = bytes.NewReader([]byte(b))
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			h.t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, h.pfx+path, rdr)
	if ifMatch != "" {
		req.Header.Set("If-Match", `"`+ifMatch+`"`)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	res := apiResult{Status: rec.Code, Header: rec.Header(), Raw: rec.Body.Bytes()}
	_ = json.Unmarshal(res.Raw, &res.Body)
	return res
}

func (h *apiHarness) revision() string { return revisionOnDisk(h.t, h.path) }

// put sends {"values": values} with the current revision.
func (h *apiHarness) put(path string, values map[string]any) apiResult {
	h.t.Helper()
	return h.do(http.MethodPut, path, h.revision(), map[string]any{"values": values})
}

func (h *apiHarness) loadConfig() *config.Config {
	h.t.Helper()
	cfg, err := config.Load(h.path)
	if err != nil {
		h.t.Fatal(err)
	}
	return cfg
}

func (h *apiHarness) fileBytes() string {
	b, _ := os.ReadFile(h.path)
	return string(b)
}

func wantStatus(t *testing.T, res apiResult, want int) {
	t.Helper()
	if res.Status != want {
		t.Fatalf("status = %d, want %d; body %s", res.Status, want, res.Raw)
	}
}

func wantCode(t *testing.T, res apiResult, code string) {
	t.Helper()
	if res.Body["code"] != code {
		t.Fatalf("code = %v, want %q; body %s", res.Body["code"], code, res.Raw)
	}
}

// ---- schema ----

func TestConfigSchemaListsEveryResource(t *testing.T) {
	h := newAPIHarness(t)
	res := h.do(http.MethodGet, "/config/schema", "", nil)
	wantStatus(t, res, http.StatusOK)
	d := res.data()
	if d["schema_version"] != float64(1) || d["name_pattern"] != "^[A-Za-z0-9._-]+$" {
		t.Errorf("header = %v / %v", d["schema_version"], d["name_pattern"])
	}
	got := map[string]map[string]any{}
	for _, r := range d["resources"].([]any) {
		m := r.(map[string]any)
		got[m["key"].(string)] = m
	}
	for _, key := range []string{"defaults", "templates", "tasks", "environments", "web", "client", "hosts", "delegation"} {
		if got[key] == nil {
			t.Errorf("schema lacks resource %q", key)
		}
	}
	tmpl := got["templates"]
	if tmpl["kind"] != "collection" || tmpl["restart"] != "agents" || tmpl["harness_options"] != true {
		t.Errorf("templates = %v", tmpl)
	}
	if acts := tmpl["actions"].([]any); len(acts) != 4 {
		t.Errorf("template actions = %v, want add/rename/duplicate/delete", acts)
	}
	subs := tmpl["subsections"].([]any)
	if len(subs) != 2 || subs[0].(map[string]any)["key"] != "permissions" || subs[1].(map[string]any)["section"] != "push_template" {
		t.Errorf("template subsections = %v", subs)
	}
	if got["web"]["restart"] != "service" || got["tasks"]["restart"] != "none" || got["defaults"]["kind"] != "singleton" {
		t.Errorf("restart/kind wrong: web=%v tasks=%v defaults=%v", got["web"], got["tasks"], got["defaults"])
	}
	field := tmpl["fields"].([]any)[0].(map[string]any)
	for _, k := range []string{"key", "label", "help", "warning", "group", "advanced", "type", "default", "constraints", "restart"} {
		if _, ok := field[k]; !ok {
			t.Errorf("field doc lacks %q: %v", k, field)
		}
	}
	harnesses := d["harnesses"].(map[string]any)
	for _, name := range []string{"claude", "codex", "opencode"} {
		if harnesses[name] == nil {
			t.Errorf("schema lacks harness %q", name)
		}
	}
}

func TestConfigSchemaServesWithoutALoadableConfig(t *testing.T) {
	h := newAPIHarness(t)
	if err := os.WriteFile(h.path, []byte("defaults: [oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, h.do(http.MethodGet, "/config/schema", "", nil), http.StatusOK)
}

// ---- reads ----

func TestGetSingletonCarriesRevisionETagAndTypedValues(t *testing.T) {
	h := newAPIHarness(t)
	res := h.do(http.MethodGet, "/config/defaults", "", nil)
	wantStatus(t, res, http.StatusOK)
	d := res.data()
	if d["revision"] != h.revision() {
		t.Errorf("revision = %v, want %v", d["revision"], h.revision())
	}
	if res.Header.Get("ETag") != `"`+h.revision()+`"` {
		t.Errorf("ETag = %q", res.Header.Get("ETag"))
	}
	vals := d["values"].(map[string]any)
	if vals["model"] != "sonnet" || vals["max_turns"] != float64(10) {
		t.Errorf("values = %v", vals)
	}
	if envs := vals["environments"].([]any); len(envs) != 1 || envs[0] != "base" {
		t.Errorf("environments = %v", vals["environments"])
	}
	if _, ok := vals["push"].(map[string]any); !ok {
		t.Errorf("defaults.push must nest under values, got %v", vals["push"])
	}
	if _, ok := vals["harness_options"].(map[string]any); !ok {
		t.Errorf("harness_options must be an object, got %v", vals["harness_options"])
	}
	if d["effective_harness"] != "claude" {
		t.Errorf("effective_harness = %v", d["effective_harness"])
	}
	if _, ok := d["inherited"]; ok {
		t.Error("defaults does not cascade; inherited must be absent")
	}
}

func TestGetCollectionListsSortedItems(t *testing.T) {
	h := newAPIHarness(t)
	res := h.do(http.MethodGet, "/config/templates", "", nil)
	wantStatus(t, res, http.StatusOK)
	items := res.data()["items"].([]any)
	if len(items) != 2 || items[0].(map[string]any)["name"] != "coder" || items[1].(map[string]any)["name"] != "reviewer" {
		t.Fatalf("items = %v", items)
	}
	coder := items[0].(map[string]any)
	vals := coder["values"].(map[string]any)
	if vals["model"] != "opus" {
		t.Errorf("coder model = %v", vals["model"])
	}
	if ho := vals["harness_options"].(map[string]any); ho["permission_mode"] != "plan" {
		t.Errorf("harness_options = %v", ho)
	}
	perms := vals["permissions"].(map[string]any)
	if cs := perms["can_spawn"].([]any); len(cs) != 1 || cs[0] != "reviewer" {
		t.Errorf("permissions = %v", perms)
	}
	if push := vals["push"].(map[string]any); push["finished"] != nil {
		t.Errorf("unset template push must be null, got %v", push["finished"])
	}
	if coder["effective_harness"] != "claude" {
		t.Errorf("effective_harness = %v", coder["effective_harness"])
	}
	reviewer := items[1].(map[string]any)
	if reviewer["inherited"].(map[string]any)["max_turns"] != nil {
		t.Errorf("unexpected inherited max_turns")
	}
}

func TestGetItemAndUnknownResourceAndName(t *testing.T) {
	h := newAPIHarness(t)
	res := h.do(http.MethodGet, "/config/tasks/heartbeat", "", nil)
	wantStatus(t, res, http.StatusOK)
	d := res.data()
	if d["name"] != "heartbeat" {
		t.Errorf("name = %v", d["name"])
	}
	vals := d["values"].(map[string]any)
	if vals["schedule"] != "0 * * * *" || vals["enabled"] != true || vals["retries"] != nil {
		t.Errorf("values = %v", vals)
	}
	if inh := d["inherited"].(map[string]any); inh["model"] != "sonnet" {
		t.Errorf("inherited = %v, want model from defaults", inh)
	}
	wantStatus(t, h.do(http.MethodGet, "/config/tasks/nope", "", nil), http.StatusNotFound)
	wantStatus(t, h.do(http.MethodGet, "/config/nonsense", "", nil), http.StatusNotFound)
	wantStatus(t, h.do(http.MethodGet, "/config/defaults/extra", "", nil), http.StatusNotFound)
}

func TestReadsOfAnUnloadableConfigAre503(t *testing.T) {
	h := newAPIHarness(t)
	if err := os.WriteFile(h.path, []byte("defaults: [oops"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := h.do(http.MethodGet, "/config/defaults", "", nil)
	wantStatus(t, res, http.StatusServiceUnavailable)
	wantCode(t, res, CodeConfigUnavailable)
}

// ---- writes: PUT ----

func TestPutRequiresIfMatch(t *testing.T) {
	h := newAPIHarness(t)
	res := h.do(http.MethodPut, "/config/templates/coder", "", map[string]any{"values": map[string]any{"model": "haiku"}})
	wantStatus(t, res, http.StatusPreconditionRequired)
	wantCode(t, res, CodeRevisionRequired)
}

func TestPutStaleRevisionIs409AndLeavesFileUntouched(t *testing.T) {
	h := newAPIHarness(t)
	before := h.fileBytes()
	res := h.do(http.MethodPut, "/config/templates/coder", "sha256:stale", map[string]any{"values": map[string]any{"model": "haiku"}})
	wantStatus(t, res, http.StatusConflict)
	wantCode(t, res, CodeRevisionMismatch)
	if res.data()["revision"] != h.revision() {
		t.Errorf("409 data = %v, want the current revision", res.data())
	}
	if h.fileBytes() != before {
		t.Error("a stale PUT changed leo.yaml")
	}
}

func TestPutReplacesPresentKeysAndKeepsAbsentOnes(t *testing.T) {
	h := newAPIHarness(t)
	rev := h.revision()
	res := h.put("/config/templates/coder", map[string]any{"model": "haiku"})
	wantStatus(t, res, http.StatusOK)
	d := res.data()
	if d["revision"] == rev || d["revision"] != h.revision() {
		t.Errorf("revision = %v, want the new on-disk revision", d["revision"])
	}
	coder := h.loadConfig().Templates["coder"]
	if coder.Model != "haiku" {
		t.Errorf("model = %q, want haiku", coder.Model)
	}
	if coder.Workspace != "/tmp/coder" || coder.HarnessOptions["permission_mode"] != "plan" || len(coder.Permissions.CanSpawn) != 1 {
		t.Errorf("absent keys were not kept: %+v", coder)
	}
	if item, ok := d["item"].(map[string]any); !ok || item["name"] != "coder" {
		t.Errorf("response item = %v", d["item"])
	}
}

func TestPutNullClearsAValue(t *testing.T) {
	h := newAPIHarness(t)
	wantStatus(t, h.put("/config/templates/coder", map[string]any{"model": nil, "max_turns": nil}), http.StatusOK)
	if got := h.loadConfig().Templates["coder"].Model; got != "" {
		t.Errorf("model = %q, want cleared", got)
	}
}

func TestPutNestedSectionsAndHarnessOptions(t *testing.T) {
	h := newAPIHarness(t)
	res := h.put("/config/templates/coder", map[string]any{
		"permissions":     map[string]any{"deny_tools": []string{"leo_spawn_agent"}},
		"push":            map[string]any{"finished": false},
		"harness_options": map[string]any{"bypass_permissions": true, "permission_mode": nil},
	})
	wantStatus(t, res, http.StatusOK)
	coder := h.loadConfig().Templates["coder"]
	if len(coder.Permissions.DenyTools) != 1 || coder.Permissions.DenyTools[0] != "leo_spawn_agent" {
		t.Errorf("permissions = %+v", coder.Permissions)
	}
	if coder.Push.Finished == nil || *coder.Push.Finished {
		t.Errorf("push.finished = %v, want false", coder.Push.Finished)
	}
	if coder.HarnessOptions["bypass_permissions"] != true {
		t.Errorf("harness_options = %v", coder.HarnessOptions)
	}
	if _, kept := coder.HarnessOptions["permission_mode"]; kept {
		t.Errorf("a null harness option must be cleared: %v", coder.HarnessOptions)
	}
	if len(coder.Permissions.CanSpawn) != 1 {
		t.Error("an absent permissions key was dropped")
	}
}

func TestPutHarnessChangeWithItsOptionsInOneWrite(t *testing.T) {
	h := newAPIHarness(t)
	res := h.put("/config/templates/reviewer", map[string]any{
		"harness":         "codex",
		"model":           "gpt-5.3-codex",
		"harness_options": map[string]any{"permission_mode": "read-only"},
	})
	wantStatus(t, res, http.StatusOK)
	r := h.loadConfig().Templates["reviewer"]
	if r.Harness != "codex" || r.HarnessOptions["permission_mode"] != "read-only" {
		t.Errorf("reviewer = %+v", r)
	}
}

func TestPutHarnessOptionTheEffectiveHarnessLacksIs422OnItsKey(t *testing.T) {
	h := newAPIHarness(t)
	before := h.fileBytes()
	res := h.put("/config/templates/coder", map[string]any{"harness_options": map[string]any{"sandbox": "read-only"}})
	wantStatus(t, res, http.StatusUnprocessableEntity)
	wantCode(t, res, CodeInvalid)
	fields := res.errorFields()
	if len(fields) != 1 || fields[0]["field"] != "harness_options.sandbox" {
		t.Errorf("errors = %v", fields)
	}
	if h.fileBytes() != before {
		t.Error("a rejected write changed leo.yaml")
	}
}

func TestPutInvalidValueIs422PerResource(t *testing.T) {
	cases := []struct {
		path   string
		values map[string]any
		field  string
	}{
		{"/config/defaults", map[string]any{"max_turns": -1}, "max_turns"},
		{"/config/templates/coder", map[string]any{"max_turns": -1}, "max_turns"},
		{"/config/tasks/heartbeat", map[string]any{"schedule": "not a cron"}, "schedule"},
		{"/config/tasks/heartbeat", map[string]any{"retries": "x"}, "retries"},
		{"/config/environments/base", map[string]any{"env": map[string]any{"bad key": "v"}}, "env"},
		{"/config/web", map[string]any{"port": 70000}, "port"},
		{"/config/hosts/remote", map[string]any{"ssh": 5}, "ssh"},
	}
	for _, tc := range cases {
		t.Run(tc.path+"/"+tc.field, func(t *testing.T) {
			h := newAPIHarness(t)
			before := h.fileBytes()
			res := h.put(tc.path, tc.values)
			wantStatus(t, res, http.StatusUnprocessableEntity)
			wantCode(t, res, CodeInvalid)
			found := false
			for _, f := range res.errorFields() {
				if f["field"] == tc.field {
					found = true
				}
			}
			if !found {
				t.Errorf("errors %v lack field %q", res.errorFields(), tc.field)
			}
			if h.fileBytes() != before {
				t.Error("an invalid write changed leo.yaml")
			}
		})
	}
}

func TestPutBadBodies(t *testing.T) {
	h := newAPIHarness(t)
	rev := h.revision()
	cases := map[string]string{
		"malformed json":     `{"values": `,
		"unknown top key":    `{"values": {}, "extra": 1}`,
		"unknown value key":  `{"values": {"not_a_field": 1}}`,
		"values not object":  `{"values": []}`,
		"missing values key": `{}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			res := h.do(http.MethodPut, "/config/templates/coder", rev, body)
			wantStatus(t, res, http.StatusBadRequest)
			wantCode(t, res, CodeBadRequest)
		})
	}
}

func TestPutUnknownItemIs404(t *testing.T) {
	h := newAPIHarness(t)
	res := h.put("/config/templates/nope", map[string]any{"model": "x"})
	wantStatus(t, res, http.StatusNotFound)
	wantCode(t, res, CodeNotFound)
}

func TestPutRestartRequiredPerResource(t *testing.T) {
	cases := []struct {
		path    string
		values  map[string]any
		service bool
		agents  bool
	}{
		{"/config/web", map[string]any{"port": 8371}, true, false},
		{"/config/templates/coder", map[string]any{"model": "haiku"}, false, true},
		{"/config/defaults", map[string]any{"max_turns": 11}, false, true},
		{"/config/environments/base", map[string]any{"env": map[string]any{"A": "2"}}, false, true},
		{"/config/tasks/heartbeat", map[string]any{"enabled": false}, false, false},
		{"/config/client", map[string]any{"default_host": "remote"}, false, false},
		{"/config/hosts/remote", map[string]any{"ssh": "me@other"}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			h := newAPIHarness(t)
			res := h.put(tc.path, tc.values)
			wantStatus(t, res, http.StatusOK)
			rr := res.data()["restart_required"].(map[string]any)
			if rr["service"] != tc.service || rr["agents"] != tc.agents {
				t.Errorf("restart_required = %v, want service=%v agents=%v", rr, tc.service, tc.agents)
			}
		})
	}
}

func TestPutTaskEnabledIsAOneKeyWrite(t *testing.T) {
	h := newAPIHarness(t)
	wantStatus(t, h.put("/config/tasks/nightly", map[string]any{"enabled": true}), http.StatusOK)
	cfg := h.loadConfig()
	if !cfg.Tasks["nightly"].Enabled || cfg.Tasks["nightly"].Schedule != "0 3 * * *" {
		t.Errorf("nightly = %+v", cfg.Tasks["nightly"])
	}
}

func TestPutReloadFailureIsAWarningNotAnError(t *testing.T) {
	svc, path := newConfigService(t, &mockReloader{err: http.ErrAbortHandler})
	h := newAPIHarnessOver(t, svc, path, "")
	res := h.put("/config/defaults", map[string]any{"max_turns": 12})
	wantStatus(t, res, http.StatusOK)
	if w, _ := res.data()["warning"].(string); w == "" {
		t.Errorf("data = %v, want a warning", res.data())
	}
	if h.loadConfig().Defaults.MaxTurns != 12 {
		t.Error("the write must stand despite the reload failure")
	}
}

func TestPutSingletons(t *testing.T) {
	h := newAPIHarness(t)
	wantStatus(t, h.put("/config/web", map[string]any{"public_urls": []string{"https://leo.example.com"}, "allowed_hosts": []string{"leo.example.com"}}), http.StatusOK)
	if got := h.loadConfig().Web.PublicURLs; len(got) != 1 {
		t.Errorf("public_urls = %v", got)
	}
	wantStatus(t, h.put("/config/client", map[string]any{"default_host": nil}), http.StatusOK)
	if h.loadConfig().Client.DefaultHost != "" {
		t.Error("default_host not cleared")
	}
}

// ---- secrets ----

func TestSecretFieldsNeverAppearInAnyResponse(t *testing.T) {
	restore := schema.MarkSecretForTest(schema.SectionClientHost, "tmux_path")
	defer restore()
	h := newAPIHarness(t)
	if err := os.WriteFile(h.path, []byte(strings.Replace(configAPIFixtureYAML, "ssh: me@box", "ssh: me@box\n      tmux_path: /hunter2/tmux", 1)), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{"/config/hosts", "/config/hosts/remote"} {
		res := h.do(http.MethodGet, p, "", nil)
		wantStatus(t, res, http.StatusOK)
		if strings.Contains(string(res.Raw), "hunter2") {
			t.Fatalf("GET %s leaked the secret: %s", p, res.Raw)
		}
	}
	item := h.do(http.MethodGet, "/config/hosts/remote", "", nil).data()["values"].(map[string]any)
	if s, ok := item["tmux_path"].(map[string]any); !ok || s["set"] != true {
		t.Errorf("secret value = %v, want {set: true}", item["tmux_path"])
	}

	res := h.put("/config/hosts/remote", map[string]any{"tmux_path": map[string]any{"replace": "/new-secret/tmux"}})
	wantStatus(t, res, http.StatusOK)
	if strings.Contains(string(res.Raw), "new-secret") {
		t.Fatalf("PUT echoed the secret: %s", res.Raw)
	}
	if h.loadConfig().Client.Hosts["remote"].TmuxPath != "/new-secret/tmux" {
		t.Error("replace did not write the value")
	}
	wantStatus(t, h.put("/config/hosts/remote", map[string]any{"tmux_path": map[string]any{"clear": true}}), http.StatusOK)
	if h.loadConfig().Client.Hosts["remote"].TmuxPath != "" {
		t.Error("clear did not clear the value")
	}
	wantStatus(t, h.put("/config/hosts/remote", map[string]any{"ssh": "me@elsewhere"}), http.StatusOK)

	if r := h.put("/config/hosts/remote", map[string]any{"tmux_path": "plain"}); r.Status != http.StatusUnprocessableEntity {
		t.Errorf("a bare secret value must be refused, got %d", r.Status)
	}
	if doc := h.do(http.MethodGet, "/config/schema", "", nil); !strings.Contains(string(doc.Raw), `"type":"secret"`) {
		t.Error("schema must type the field as secret")
	}
}

// ---- add / rename / duplicate / delete ----

func TestPostAddCreatesItemsPerCollection(t *testing.T) {
	cases := []struct {
		resource string
		body     map[string]any
		check    func(*config.Config) bool
	}{
		{"templates", map[string]any{"name": "fresh"}, func(c *config.Config) bool { _, ok := c.Templates["fresh"]; return ok }},
		{"tasks", map[string]any{"name": "fresh", "schedule": "5 * * * *"}, func(c *config.Config) bool {
			t, ok := c.Tasks["fresh"]
			return ok && t.PromptFile == "prompts/fresh.md" && !t.Enabled
		}},
		{"environments", map[string]any{"name": "fresh"}, func(c *config.Config) bool { _, ok := c.Environments["fresh"]; return ok }},
		{"hosts", map[string]any{"name": "fresh"}, func(c *config.Config) bool { _, ok := c.Client.Hosts["fresh"]; return ok }},
	}
	for _, tc := range cases {
		t.Run(tc.resource, func(t *testing.T) {
			h := newAPIHarness(t)
			res := h.do(http.MethodPost, "/config/"+tc.resource, h.revision(), tc.body)
			wantStatus(t, res, http.StatusCreated)
			d := res.data()
			if item, ok := d["item"].(map[string]any); !ok || item["name"] != "fresh" {
				t.Errorf("item = %v", d["item"])
			}
			if d["revision"] != h.revision() {
				t.Errorf("revision = %v, want on-disk %v", d["revision"], h.revision())
			}
			if !tc.check(h.loadConfig()) {
				t.Error("item not created as specified")
			}
		})
	}
}

func TestPostAddRejectionsAre422OnName(t *testing.T) {
	h := newAPIHarness(t)
	before := h.fileBytes()
	for _, tc := range []struct {
		resource string
		body     map[string]any
		field    string
	}{
		{"templates", map[string]any{"name": "coder"}, "name"},
		{"templates", map[string]any{"name": "bad name"}, "name"},
		{"tasks", map[string]any{"name": "fresh"}, "schedule"},
		{"tasks", map[string]any{"name": "heartbeat", "schedule": "* * * * *"}, "name"},
		{"environments", map[string]any{"name": "base"}, "name"},
		{"hosts", map[string]any{"name": "remote"}, "name"},
	} {
		res := h.do(http.MethodPost, "/config/"+tc.resource, h.revision(), tc.body)
		wantStatus(t, res, http.StatusUnprocessableEntity)
		if f := res.errorFields(); len(f) == 0 || f[0]["field"] != tc.field {
			t.Errorf("%s %v: errors = %v, want field %q", tc.resource, tc.body, f, tc.field)
		}
	}
	if h.fileBytes() != before {
		t.Error("a rejected add changed leo.yaml")
	}
}

func TestPostAddNeedsRevisionAndIsRefusedOnSingletons(t *testing.T) {
	h := newAPIHarness(t)
	wantStatus(t, h.do(http.MethodPost, "/config/templates", "", map[string]any{"name": "x"}), http.StatusPreconditionRequired)
	wantStatus(t, h.do(http.MethodPost, "/config/templates", "sha256:stale", map[string]any{"name": "x"}), http.StatusConflict)
	wantStatus(t, h.do(http.MethodPost, "/config/web", h.revision(), map[string]any{"name": "x"}), http.StatusNotFound)
	wantStatus(t, h.do(http.MethodPost, "/config/tasks", h.revision(), `{"name":"x","schedule":"* * * * *","bogus":1}`), http.StatusBadRequest)
}

func TestRenameTemplateAndEnvironment(t *testing.T) {
	h := newAPIHarness(t)
	res := h.do(http.MethodPost, "/config/templates/reviewer/rename", h.revision(), map[string]any{"new_name": "checker"})
	wantStatus(t, res, http.StatusOK)
	cfg := h.loadConfig()
	if _, ok := cfg.Templates["checker"]; !ok || cfg.Delegation.Profiles["fast"].Roles["coder"].Template != "checker" {
		t.Error("template rename did not rewrite references")
	}
	if item := res.data()["item"].(map[string]any); item["name"] != "checker" {
		t.Errorf("item = %v", item)
	}

	res = h.do(http.MethodPost, "/config/environments/base/rename", h.revision(), map[string]any{"new_name": "root"})
	wantStatus(t, res, http.StatusOK)
	cfg = h.loadConfig()
	if _, ok := cfg.Environments["root"]; !ok || cfg.Defaults.Environments[0] != "root" {
		t.Error("environment rename did not rewrite references")
	}

	for _, tc := range []struct{ path, newName string }{
		{"/config/templates/coder/rename", "checker"},
		{"/config/templates/coder/rename", "bad name"},
		{"/config/environments/root/rename", "root"},
	} {
		res := h.do(http.MethodPost, tc.path, h.revision(), map[string]any{"new_name": tc.newName})
		wantStatus(t, res, http.StatusUnprocessableEntity)
		if f := res.errorFields(); len(f) == 0 || f[0]["field"] != "new_name" {
			t.Errorf("%s -> %q: errors = %v", tc.path, tc.newName, f)
		}
	}
	wantStatus(t, h.do(http.MethodPost, "/config/templates/ghost/rename", h.revision(), map[string]any{"new_name": "x"}), http.StatusNotFound)
	wantStatus(t, h.do(http.MethodPost, "/config/tasks/heartbeat/rename", h.revision(), map[string]any{"new_name": "x"}), http.StatusNotFound)
	wantStatus(t, h.do(http.MethodPost, "/config/templates/coder/rename", "sha256:stale", map[string]any{"new_name": "z"}), http.StatusConflict)
	wantStatus(t, h.do(http.MethodPost, "/config/templates/coder/rename", "", map[string]any{"new_name": "z"}), http.StatusPreconditionRequired)
}

func TestDuplicateTemplate(t *testing.T) {
	h := newAPIHarness(t)
	res := h.do(http.MethodPost, "/config/templates/coder/duplicate", h.revision(), map[string]any{"new_name": "coder2"})
	wantStatus(t, res, http.StatusCreated)
	copy2 := h.loadConfig().Templates["coder2"]
	if copy2.Model != "opus" || copy2.HarnessOptions["permission_mode"] != "plan" || len(copy2.Permissions.CanSpawn) != 1 {
		t.Errorf("duplicate lost data: %+v", copy2)
	}
	wantStatus(t, h.do(http.MethodPost, "/config/templates/coder/duplicate", h.revision(), map[string]any{"new_name": "reviewer"}), http.StatusUnprocessableEntity)
	wantStatus(t, h.do(http.MethodPost, "/config/templates/ghost/duplicate", h.revision(), map[string]any{"new_name": "g"}), http.StatusNotFound)
	wantStatus(t, h.do(http.MethodPost, "/config/templates/coder/duplicate", "", map[string]any{"new_name": "g"}), http.StatusPreconditionRequired)
	wantStatus(t, h.do(http.MethodPost, "/config/tasks/heartbeat/duplicate", h.revision(), map[string]any{"new_name": "g"}), http.StatusNotFound)
}

func TestDeleteItems(t *testing.T) {
	h := newAPIHarness(t)
	wantStatus(t, h.do(http.MethodDelete, "/config/tasks/nightly", h.revision(), nil), http.StatusOK)
	if _, ok := h.loadConfig().Tasks["nightly"]; ok {
		t.Error("task not deleted")
	}
	wantStatus(t, h.do(http.MethodDelete, "/config/hosts/remote", h.revision(), nil), http.StatusOK)

	wantStatus(t, h.do(http.MethodDelete, "/config/tasks/nope", h.revision(), nil), http.StatusNotFound)
	wantStatus(t, h.do(http.MethodDelete, "/config/tasks/heartbeat", "", nil), http.StatusPreconditionRequired)
	wantStatus(t, h.do(http.MethodDelete, "/config/tasks/heartbeat", "sha256:stale", nil), http.StatusConflict)
	wantStatus(t, h.do(http.MethodDelete, "/config/web", h.revision(), nil), http.StatusNotFound)

	before := h.fileBytes()
	res := h.do(http.MethodDelete, "/config/environments/base", h.revision(), nil)
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if !strings.Contains(string(res.Raw), "base") {
		t.Errorf("the 422 must name the reference: %s", res.Raw)
	}
	res = h.do(http.MethodDelete, "/config/templates/reviewer", h.revision(), nil)
	wantStatus(t, res, http.StatusUnprocessableEntity)
	if h.fileBytes() != before {
		t.Error("a refused delete changed leo.yaml")
	}
}
