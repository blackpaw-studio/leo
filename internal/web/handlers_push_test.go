package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var pushKeys = []string{"needs_input", "finished", "errored"}

// The push groups are nested structs the flat registry cannot reach, so
// each is rendered from its own section; without that wiring the inputs
// never appear.
func TestPushFieldsRenderOnDefaultsAndTemplatePages(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)
	for _, page := range []string{"/config/defaults", "/config/templates/coding"} {
		req := httptest.NewRequest(http.MethodGet, page, nil)
		w := httptest.NewRecorder()
		s.httpServer.Handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", page, w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "Push notifications") {
			t.Errorf("%s: missing the Push notifications group", page)
		}
		for _, key := range pushKeys {
			if !strings.Contains(body, `name="`+key+`"`) {
				t.Errorf("%s: missing the %q input", page, key)
			}
		}
	}
}

// Unset reads as on, so a fresh Defaults page shows every toggle checked.
func TestDefaultsPushTogglesShowUnsetAsOn(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/config/defaults", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	body := w.Body.String()
	for _, key := range pushKeys {
		re := regexp.MustCompile(`<input type="checkbox" id="f-` + key + `" name="` + key + `" value="true" checked>`)
		if !re.MatchString(body) {
			t.Errorf("%s toggle is not checked while unset", key)
		}
	}
}

func TestDefaultsSaveRoundTripsPush(t *testing.T) {
	s, dir := newTestServer(t)
	form := url.Values{}
	form.Set("model", "sonnet")
	form.Add("needs_input", "false")
	form.Add("needs_input", "true") // checked
	form.Add("finished", "false")   // unchecked
	form.Add("errored", "false")
	form.Add("errored", "true")

	if w := postForm(t, s, "/web/config/defaults", form); w.Code != http.StatusOK {
		t.Fatalf("save: %d, body=%s", w.Code, readBody(t, w))
	}

	cfg := reloadTestConfig(t, dir)
	if cfg.PushEnabled("", "finished") {
		t.Error("finished toggle off did not stick")
	}
	if !cfg.PushEnabled("", "needs_input") || !cfg.PushEnabled("", "errored") {
		t.Error("checked toggles must stay on")
	}
	// On is the default, so a checked toggle leaves no key behind.
	raw, _ := os.ReadFile(filepath.Join(dir, "leo.yaml"))
	if strings.Contains(string(raw), "needs_input") || !strings.Contains(string(raw), "finished: false") {
		t.Errorf("yaml should hold only the explicit off:\n%s", raw)
	}
}

func TestTemplateSaveRoundTripsPushAndInheritRemovesKey(t *testing.T) {
	s, dir, _ := newTestServerWithAgents(t)

	form := templateFormBase(t, s, "coding")
	form.Set("needs_input", "true")
	form.Set("finished", "false")
	form.Set("errored", "")
	if w := postForm(t, s, "/web/config/template/coding", form); w.Code != http.StatusOK {
		t.Fatalf("save: %d, body=%s", w.Code, readBody(t, w))
	}
	push := reloadTestConfig(t, dir).Templates["coding"].Push
	if push.NeedsInput == nil || !*push.NeedsInput || push.Finished == nil || *push.Finished || push.Errored != nil {
		t.Fatalf("saved push = %+v, want needs_input on, finished off, errored unset", push)
	}

	inherit := templateFormBase(t, s, "coding")
	inherit.Set("needs_input", "")
	inherit.Set("finished", "")
	inherit.Set("errored", "")
	if w := postForm(t, s, "/web/config/template/coding", inherit); w.Code != http.StatusOK {
		t.Fatalf("inherit save: %d, body=%s", w.Code, readBody(t, w))
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "leo.yaml"))
	for _, key := range pushKeys {
		if strings.Contains(string(raw), key+":") {
			t.Errorf("inherit must remove %q from the YAML:\n%s", key, raw)
		}
	}
}
