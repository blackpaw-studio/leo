package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestPageConfigTemplatesShowsTemplates(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	req := httptest.NewRequest("GET", "/config/templates", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "coding") {
		t.Error("expected body to contain template name 'coding'")
	}
	if !strings.Contains(body, "research") {
		t.Error("expected body to contain template name 'research'")
	}
	if !strings.Contains(body, "New template") {
		t.Error("expected body to contain 'New template' form")
	}
}

// TestTemplateAddRedirectsToEdit exercises the add-then-edit flow: adding a
// template creates a bare (name-only) entry and 303s straight to its edit
// page, mirroring TestProcessAddRedirectsToEdit/TestTaskAddRedirectsToEdit.
// Rewritten in Task 9 from the old TestTemplateAdd, which pinned the
// hand-rolled handleTemplateAdd's per-field contract (workspace/model
// persisted on add, 200 response with a rendered fragment) — add is now
// name-only, matching every other schema-driven section.
func TestTemplateAddRedirectsToEdit(t *testing.T) {
	s, dir, _ := newTestServerWithAgents(t)

	form := url.Values{}
	form.Set("name", "deploy")

	req := httptest.NewRequest("POST", "/web/template/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/config/templates/deploy" {
		t.Errorf("add: status=%d loc=%q", w.Code, w.Header().Get("Location"))
	}

	// Verify config was persisted
	cfg := loadConfigFromDisk(t, dir)
	if _, ok := cfg.Templates["deploy"]; !ok {
		t.Fatal("expected 'deploy' template in saved config")
	}
}

func TestTemplateAddDuplicate(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	form := url.Values{}
	form.Set("name", "coding") // already exists
	form.Set("model", "sonnet")

	req := httptest.NewRequest("POST", "/web/template/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "already exists") {
		t.Error("expected error about duplicate template")
	}
}

func TestTemplateAddEmptyName(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	form := url.Values{}
	form.Set("name", "")

	req := httptest.NewRequest("POST", "/web/template/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "flash-error") {
		t.Error("expected error about empty name")
	}
}

func TestTemplateEdit(t *testing.T) {
	s, dir, _ := newTestServerWithAgents(t)

	form := url.Values{}
	form.Set("model", "haiku")
	form.Set("workspace", "/tmp/updated")
	form.Set("max_turns", "100")
	form.Set("channels", "plugin:slack@official")

	req := httptest.NewRequest("POST", "/web/config/template/coding", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !strings.Contains(body, "saved") {
		t.Error("expected success flash with 'saved'")
	}

	// Verify config was persisted
	cfg := loadConfigFromDisk(t, dir)
	tmpl, ok := cfg.Templates["coding"]
	if !ok {
		t.Fatal("expected 'coding' template in saved config")
	}
	if tmpl.Model != "haiku" {
		t.Errorf("model: got %q, want 'haiku'", tmpl.Model)
	}
	if tmpl.Workspace != "/tmp/updated" {
		t.Errorf("workspace: got %q, want '/tmp/updated'", tmpl.Workspace)
	}
	if tmpl.MaxTurns != 100 {
		t.Errorf("max_turns: got %d, want 100", tmpl.MaxTurns)
	}
}

// TestTemplateEditNotFound is updated in Task 9 for the schema-driven save
// path: applySection's generic not-found branch renders "Not found" (no
// template name, capitalized), replacing the old hand-rolled
// handleConfigTemplate's "Template %q not found".
func TestTemplateEditNotFound(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	form := url.Values{}
	form.Set("model", "sonnet")

	req := httptest.NewRequest("POST", "/web/config/template/nonexistent", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Not found") {
		t.Error("expected error about template not found")
	}
}

// TestTemplateDeleteRedirectsToList guards the edit page's delete button: on
// success the handler must send HX-Redirect: /config/templates so htmx
// navigates the browser back to the list, mirroring
// TestProcessDeleteRedirectsToList/TestTaskDeleteRedirectsToList. Rewritten
// in Task 9 from the old TestTemplateDelete, which pinned the hand-rolled
// handleTemplateDelete's rendered-fragment response instead of a redirect.
func TestTemplateDeleteRedirectsToList(t *testing.T) {
	s, dir, _ := newTestServerWithAgents(t)

	req := httptest.NewRequest("DELETE", "/web/template/coding", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("HX-Redirect"); loc != "/config/templates" {
		t.Errorf("HX-Redirect = %q, want /config/templates", loc)
	}

	// Verify config was persisted without the deleted template
	cfg := loadConfigFromDisk(t, dir)
	if _, ok := cfg.Templates["coding"]; ok {
		t.Fatal("expected 'coding' template to be removed from config")
	}
	// Other template should still exist
	if _, ok := cfg.Templates["research"]; !ok {
		t.Fatal("expected 'research' template to still exist")
	}
}

func TestTemplateDeleteNotFound(t *testing.T) {
	s, _, _ := newTestServerWithAgents(t)

	req := httptest.NewRequest("DELETE", "/web/template/nonexistent", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "not found") {
		t.Error("expected error about template not found")
	}
}

// loadConfigFromDisk reads and parses the config file from disk.
func loadConfigFromDisk(t *testing.T, dir string) *config.Config {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "leo.yaml"))
	if err != nil {
		t.Fatalf("reading config: %v", err)
	}
	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parsing config: %v", err)
	}
	return &cfg
}
