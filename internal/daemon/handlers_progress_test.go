package daemon

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/blackpaw-studio/leo/internal/observe"
)

func TestSocketDismissesEndedTasks(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "leo.sock"), filepath.Join(dir, "leo.yaml"), nil)
	store := observe.NewProgressStore(nil, nil)
	title := "Tests"
	for id, state := range map[string]observe.TaskState{"bad": observe.TaskFailed, "ok": observe.TaskSucceeded, "off": observe.TaskCancelled, "run": observe.TaskRunning} {
		if _, err := store.Upsert("alfred", observe.TaskUpdate{ID: id, Title: &title, State: state}); err != nil {
			t.Fatal(err)
		}
	}
	s.SetProgress(store)

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/agents/alfred/tasks/run", http.StatusConflict},
		{"/agents/alfred/tasks/nope", http.StatusNotFound},
		{"/agents/ghost/tasks/bad", http.StatusNotFound},
		{"/agents/alfred/tasks/bad", http.StatusNoContent},
		{"/agents/alfred/tasks/bad", http.StatusNotFound}, // already gone
		{"/agents/alfred/tasks/ok", http.StatusNoContent},
		{"/agents/alfred/tasks/off", http.StatusNoContent},
	} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodDelete, tc.path, nil))
		if w.Code != tc.want {
			t.Errorf("DELETE %s = %d, want %d; body = %s", tc.path, w.Code, tc.want, w.Body.String())
		}
	}
	if got := store.Tasks("alfred"); len(got) != 1 || got[0].ID != "run" {
		t.Fatalf("tasks = %+v", got)
	}
}
