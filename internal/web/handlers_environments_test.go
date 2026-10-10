package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func getRequest(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestEnvironmentAddSaveAndPageRender(t *testing.T) {
	s, dir := newTestServer(t)

	w := postForm(t, s, "/web/environment/add", url.Values{"name": {"work"}})
	if w.Code != http.StatusOK {
		t.Fatalf("add: %d %s", w.Code, readBody(t, w))
	}
	if _, ok := reloadTestConfig(t, dir).Environments["work"]; !ok {
		t.Fatal("environment not created")
	}

	w = postForm(t, s, "/web/config/environment/work", url.Values{"env": {"CLAUDE_CONFIG_DIR=/Users/me/.claude-work\nFOO=bar"}})
	if body := readBody(t, w); strings.Contains(body, "flash-error") {
		t.Fatalf("save failed: %s", body)
	}
	got := reloadTestConfig(t, dir).Environments["work"]
	if got["CLAUDE_CONFIG_DIR"] != "/Users/me/.claude-work" || got["FOO"] != "bar" {
		t.Fatalf("saved env = %v", got)
	}

	page := readBody(t, getRequest(t, s, "/config/environments"))
	for _, want := range []string{"work", "CLAUDE_CONFIG_DIR=/Users/me/.claude-work"} {
		if !strings.Contains(page, want) {
			t.Errorf("page missing %q", want)
		}
	}
}

func TestEnvironmentAddRejectsDuplicateAndBadName(t *testing.T) {
	s, dir := newTestServer(t)
	postForm(t, s, "/web/environment/add", url.Values{"name": {"work"}})
	for _, name := range []string{"work", "", "bad name!"} {
		if body := readBody(t, postForm(t, s, "/web/environment/add", url.Values{"name": {name}})); !strings.Contains(body, "flash-error") {
			t.Errorf("name %q: want error flash, got %s", name, body)
		}
	}
	if n := len(reloadTestConfig(t, dir).Environments); n != 1 {
		t.Errorf("environments = %d, want 1", n)
	}
}

func TestEnvironmentSaveRejectsMalformedEnvLine(t *testing.T) {
	s, dir := newTestServer(t)
	postForm(t, s, "/web/environment/add", url.Values{"name": {"work"}})
	body := readBody(t, postForm(t, s, "/web/config/environment/work", url.Values{"env": {"no-equals-sign"}}))
	if !strings.Contains(body, "flash-error") {
		t.Fatalf("want error flash, got %s", body)
	}
	if got := reloadTestConfig(t, dir).Environments["work"]; len(got) != 0 {
		t.Fatalf("malformed save must not persist: %v", got)
	}
}

func TestEnvironmentDeleteBlockedWhileReferenced(t *testing.T) {
	s, dir := newTestServer(t)
	postForm(t, s, "/web/environment/add", url.Values{"name": {"work"}})
	postForm(t, s, "/web/config/defaults", url.Values{"environments": {"work"}})
	if got := reloadTestConfig(t, dir).Defaults.Environments; len(got) != 1 {
		t.Fatalf("setup: defaults.environments = %v", got)
	}

	body := readBody(t, deleteRequest(t, s, "/web/environment/work"))
	if !strings.Contains(body, "flash-error") {
		t.Fatalf("want validation error, got %s", body)
	}
	if _, ok := reloadTestConfig(t, dir).Environments["work"]; !ok {
		t.Fatal("referenced environment was deleted")
	}

	postForm(t, s, "/web/config/defaults", url.Values{"environments": {""}})
	if w := deleteRequest(t, s, "/web/environment/work"); w.Code != http.StatusOK {
		t.Fatalf("unreferenced delete: %d", w.Code)
	}
	if _, ok := reloadTestConfig(t, dir).Environments["work"]; ok {
		t.Fatal("environment not deleted")
	}
}
