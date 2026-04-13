package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHandleTaskAdd_WarnsOnMissingPromptFile ensures that creating a task
// whose prompt file doesn't exist succeeds (save goes through) but returns a
// warning flash so the user knows they still have to author the prompt.
func TestHandleTaskAdd_WarnsOnMissingPromptFile(t *testing.T) {
	s, _ := newTestServer(t)

	form := url.Values{}
	form.Set("name", "missing-prompt-task")
	form.Set("schedule", "0 * * * *")
	form.Set("prompt_file", "does-not-exist.md")
	form.Set("enabled", "true")

	req := httptest.NewRequest(http.MethodPost, "/web/task/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleTaskAdd(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if !strings.Contains(body, "prompt file") && !strings.Contains(body, "does not exist") {
		t.Errorf("response should warn about missing prompt file; body = %q", body)
	}
	if !strings.Contains(body, "flash-warning") && !strings.Contains(body, `class="flash flash-warning"`) {
		// Flash type should be warning (not success) when prompt missing
		t.Errorf("response should render warning flash type; body = %q", body)
	}
}

// TestHandleTaskAdd_SuccessWhenPromptFileExists covers the positive case.
func TestHandleTaskAdd_SuccessWhenPromptFileExists(t *testing.T) {
	s, dir := newTestServer(t)

	// Create a prompt file inside the default workspace.
	ws := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(ws, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "exists.md"), []byte("# hi"), 0600); err != nil {
		t.Fatal(err)
	}

	form := url.Values{}
	form.Set("name", "ok-task")
	form.Set("schedule", "0 * * * *")
	form.Set("prompt_file", "exists.md")
	form.Set("enabled", "true")

	req := httptest.NewRequest(http.MethodPost, "/web/task/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleTaskAdd(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "does not exist") {
		t.Errorf("response should not warn about missing prompt; body = %q", body)
	}
}
