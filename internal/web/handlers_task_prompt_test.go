package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// TestHandleTaskAdd_RedirectsWithAutoPromptFile covers the add-then-edit
// flow's happy path: a new task needs only a name and schedule, gets an
// auto-named (not-yet-existing) prompt file and starts disabled, and the
// response redirects straight to its edit page rather than rendering a
// flash — the add form is a plain, non-htmx-boosted POST.
func TestHandleTaskAdd_RedirectsWithAutoPromptFile(t *testing.T) {
	s, dir := newTestServer(t)

	form := url.Values{}
	form.Set("name", "fresh-task")
	form.Set("schedule", "0 * * * *")

	req := httptest.NewRequest(http.MethodPost, "/web/task/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleTaskAdd(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body = %s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/tasks/fresh-task" {
		t.Errorf("Location = %q, want /tasks/fresh-task", loc)
	}

	cfg, err := config.Load(filepath.Join(dir, "leo.yaml"))
	if err != nil {
		t.Fatalf("reloading config: %v", err)
	}
	task, ok := cfg.Tasks["fresh-task"]
	if !ok {
		t.Fatal("task not created")
	}
	if task.PromptFile != "prompts/fresh-task.md" {
		t.Errorf("PromptFile = %q, want prompts/fresh-task.md", task.PromptFile)
	}
	if task.Enabled {
		t.Error("newly added task should start disabled until its prompt is authored")
	}
}

// TestHandleTaskAdd_RejectsMissingSchedule keeps the schedule-required
// validation from the pre-rewrite handler.
func TestHandleTaskAdd_RejectsMissingSchedule(t *testing.T) {
	s, _ := newTestServer(t)

	form := url.Values{}
	form.Set("name", "no-schedule")

	req := httptest.NewRequest(http.MethodPost, "/web/task/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleTaskAdd(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (flash response); body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Schedule is required") {
		t.Errorf("expected schedule-required flash; body = %q", w.Body.String())
	}
}
