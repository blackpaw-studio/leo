package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
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

func seedEnvironmentRenameFixture(t *testing.T) (*Server, string) {
	t.Helper()
	s, dir := newTestServer(t)
	for _, name := range []string{"work", "home"} {
		postForm(t, s, "/web/environment/add", url.Values{"name": {name}})
	}
	postForm(t, s, "/web/config/defaults", url.Values{"environments": {"home,work"}})
	return s, dir
}

func TestEnvironmentRenameRewritesConfigAndAgentRecords(t *testing.T) {
	s, dir := seedEnvironmentRenameFixture(t)
	if err := agentstore.Save(dir, agentstore.Record{Name: "a1", Environments: []string{"home", "work"}}); err != nil {
		t.Fatalf("seeding agentstore: %v", err)
	}
	if err := agentstore.Save(dir, agentstore.Record{Name: "a2", Environments: []string{"home"}}); err != nil {
		t.Fatalf("seeding agentstore: %v", err)
	}

	s.agentsRestartNeeded.Store(false) // seeding defaults raised it; only the rename is under test
	w := postForm(t, s, "/web/environment/work/rename", url.Values{"new_name": {"job"}})
	if w.Code != http.StatusOK {
		t.Fatalf("rename: %d %s", w.Code, readBody(t, w))
	}
	if got := w.Header().Get("HX-Refresh"); got != "true" {
		t.Errorf("HX-Refresh = %q, want true", got)
	}

	cfg := reloadTestConfig(t, dir)
	if _, ok := cfg.Environments["work"]; ok {
		t.Error("old key still present")
	}
	if _, ok := cfg.Environments["job"]; !ok {
		t.Error("new key missing")
	}
	if got := cfg.Defaults.Environments; !slices.Equal(got, []string{"home", "job"}) {
		t.Errorf("defaults.environments = %v", got)
	}

	records, err := agentstore.Load(agentstore.FilePath(dir))
	if err != nil {
		t.Fatalf("loading agentstore: %v", err)
	}
	if got := records["a1"].Environments; !slices.Equal(got, []string{"home", "job"}) {
		t.Errorf("a1 environments = %v", got)
	}
	if got := records["a2"].Environments; !slices.Equal(got, []string{"home"}) {
		t.Errorf("a2 environments = %v, want unchanged", got)
	}
	if s.agentsRestartNeeded.Load() {
		t.Error("rename must not raise the restart banner: env contents are unchanged")
	}
}

func TestEnvironmentRenameRejections(t *testing.T) {
	tests := []struct {
		name, path, newName, want string
	}{
		{"empty", "work", "", "New name is required"},
		{"bad name", "work", "bad name!", entityNameError},
		{"collision", "work", "home", "already exists"},
		{"same name", "work", "work", "same"},
		{"unknown old", "ghost", "fresh", "not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, dir := seedEnvironmentRenameFixture(t)
			w := postForm(t, s, "/web/environment/"+tc.path+"/rename", url.Values{"new_name": {tc.newName}})
			body := readBody(t, w)
			if !strings.Contains(body, "flash-error") || !strings.Contains(body, tc.want) {
				t.Fatalf("want error flash containing %q, got %s", tc.want, body)
			}
			if got := reloadTestConfig(t, dir).Environments; len(got) != 2 {
				t.Errorf("config changed on rejected rename: %v", got)
			}
		})
	}
}
