package web

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/secretenv"
)

// markerRef is a command reference that, if anything resolved it, would
// create marker: a web path that reaches a resolver fails the test.
func markerRef(t *testing.T) (secretenv.Ref, string) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "resolved")
	return secretenv.Ref{Backend: secretenv.Command, Argv: []string{"/bin/touch", marker}}, marker
}

func assertNotResolved(t *testing.T, marker string) {
	t.Helper()
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a web path resolved a secret reference")
	}
}

// seedRefs stores a reference under OP_TOKEN in the base environment, the
// coder template and the heartbeat task.
func seedRefs(t *testing.T, h *apiHarness, ref secretenv.Ref) {
	t.Helper()
	cfg := h.loadConfig()
	cfg.Environments["base"]["OP_TOKEN"] = config.Secret(ref)
	tmpl := cfg.Templates["coder"]
	tmpl.Env = config.EnvMap{"OP_TOKEN": config.Secret(ref), "REGION": config.Literal("us")}
	cfg.Templates["coder"] = tmpl
	task := cfg.Tasks["heartbeat"]
	task.Env = config.EnvMap{"OP_TOKEN": config.Secret(ref)}
	cfg.Tasks["heartbeat"] = task
	if err := config.Save(h.path, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestConfigAPIReadsRefsAsSetWithRef(t *testing.T) {
	h := newAPIHarness(t)
	ref, marker := markerRef(t)
	seedRefs(t, h, ref)
	want := map[string]any{"set": true, "ref": map[string]any{"secret": "command", "argv": []any{"/bin/touch", marker}}}
	for _, path := range []string{"/config/environments/base", "/config/templates/coder", "/config/tasks/heartbeat"} {
		env, _ := h.readValues(path)["env"].(map[string]any)
		if !reflect.DeepEqual(env["OP_TOKEN"], want) {
			t.Errorf("%s: env.OP_TOKEN = %#v, want %#v", path, env["OP_TOKEN"], want)
		}
	}
	if env := h.readValues("/config/templates/coder")["env"].(map[string]any); env["REGION"] != "us" {
		t.Errorf("literal entry changed shape: %v", env)
	}
	assertNotResolved(t, marker)
}

func TestConfigAPIEchoBackKeepsRefBytes(t *testing.T) {
	h := newAPIHarness(t)
	ref, marker := markerRef(t)
	seedRefs(t, h, ref)
	before := h.fileBytes()
	for _, path := range []string{"/config/environments/base", "/config/templates/coder", "/config/tasks/heartbeat"} {
		env := h.readValues(path)["env"]
		wantStatus(t, h.put(path, map[string]any{"env": env}), http.StatusOK)
	}
	// {"set": true} alone keeps a reference too.
	wantStatus(t, h.put("/config/templates/coder", map[string]any{"env": map[string]any{"OP_TOKEN": map[string]any{"set": true}}}), http.StatusOK)
	if after := h.fileBytes(); after != before {
		t.Errorf("echo-back changed leo.yaml:\n--- before\n%s\n--- after\n%s", before, after)
	}
	assertNotResolved(t, marker)
}

func TestConfigAPISetsAndRejectsRefs(t *testing.T) {
	h := newAPIHarness(t)
	set := map[string]any{"ref": map[string]any{"secret": "keychain", "service": "OP", "account": "evan"}}
	wantStatus(t, h.put("/config/templates/coder", map[string]any{"env": map[string]any{"OP_TOKEN": set}}), http.StatusOK)
	got, ok := h.loadConfig().Templates["coder"].Env["OP_TOKEN"].Ref()
	if !ok || got.Service != "OP" || got.Account != "evan" {
		t.Fatalf("stored = %v, %v", got, ok)
	}
	if !strings.Contains(h.fileBytes(), "secret: keychain") {
		t.Errorf("leo.yaml lacks the ref:\n%s", h.fileBytes())
	}

	for name, bad := range map[string]any{
		"unknown backend": map[string]any{"ref": map[string]any{"secret": "vault"}},
		"missing key":     map[string]any{"ref": map[string]any{"secret": "keychain"}},
		"extra key":       map[string]any{"ref": map[string]any{"secret": "file", "path": "/x", "mode": "0600"}},
		"ref and set":     map[string]any{"set": true, "ref": map[string]any{"secret": "file", "path": "/x"}, "extra": 1},
	} {
		res := h.put("/config/templates/coder", map[string]any{"env": map[string]any{"OP_TOKEN": bad}})
		wantStatus(t, res, http.StatusUnprocessableEntity)
		if f := res.errorFields(); len(f) != 1 || f[0]["field"] != "env" {
			t.Errorf("%s: errors = %v", name, f)
		}
	}

	// A string replaces the reference with a literal.
	wantStatus(t, h.put("/config/templates/coder", map[string]any{"env": map[string]any{"OP_TOKEN": "literal"}}), http.StatusOK)
	if v, ok := h.loadConfig().Templates["coder"].Env["OP_TOKEN"].AsLiteral(); !ok || v != "literal" {
		t.Errorf("after string: %v", h.loadConfig().Templates["coder"].Env["OP_TOKEN"])
	}
}

func TestEnvFormKeepsRefsAndShowsDescriptor(t *testing.T) {
	ref, marker := markerRef(t)
	cfg := &config.Config{
		Environments: map[string]config.EnvMap{"work": {"OP_TOKEN": config.Secret(ref), "OLD": config.Secret(ref), "FOO": config.Literal("bar")}},
	}
	s, cfgPath := newTestServerWithConfigFile(t, cfg)
	dir := filepath.Dir(cfgPath)

	page := readBody(t, getRequest(t, s, "/config/environments"))
	if !strings.Contains(page, "OP_TOKEN") || !strings.Contains(page, "&lt;secret command(argv=[/bin/touch") {
		t.Errorf("page lacks the ref descriptor:\n%s", page)
	}
	if strings.Contains(page, "OP_TOKEN=") {
		t.Errorf("the textarea lists a ref as a literal line")
	}

	// The textarea names FOO and replaces OLD with a literal; OP_TOKEN is not
	// named, so its reference carries over.
	w := postForm(t, s, "/web/config/environment/work", url.Values{"env": {"FOO=baz\nOLD=plain"}})
	if body := readBody(t, w); strings.Contains(body, "flash-error") {
		t.Fatalf("save failed: %s", body)
	}
	got := reloadTestConfig(t, dir).Environments["work"]
	if r, ok := got["OP_TOKEN"].Ref(); !ok || r.Backend != secretenv.Command {
		t.Errorf("OP_TOKEN = %v, want the ref kept", got["OP_TOKEN"])
	}
	if got["FOO"] != config.Literal("baz") || got["OLD"] != config.Literal("plain") {
		t.Errorf("env = %v", got)
	}
	assertNotResolved(t, marker)
}
