package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRestartBanner_HiddenByDefault verifies the restart banner is not shown
// when no process-affecting config changes have been saved.
func TestRestartBanner_HiddenByDefault(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/partials/status", nil)
	w := httptest.NewRecorder()
	s.handlePartialStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "restart-banner") {
		t.Errorf("banner should be hidden when restartNeeded=false; body = %q", body)
	}
}

// TestRestartBanner_ShownWhenRestartNeeded verifies the banner renders with a
// prominent warning + one-click restart button when restartNeeded=true.
func TestRestartBanner_ShownWhenRestartNeeded(t *testing.T) {
	s, _ := newTestServer(t)
	s.restartNeeded.Store(true)

	req := httptest.NewRequest(http.MethodGet, "/partials/status", nil)
	w := httptest.NewRecorder()
	s.handlePartialStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()

	for _, want := range []string{
		`class="restart-banner"`,
		`role="alert"`,
		"Restart required",
		`hx-post="/web/service/restart"`,
		"Restart Now",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("banner missing %q; body = %q", want, body)
		}
	}
}
