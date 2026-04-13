package web

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestAppendReloadWarning_Passthrough verifies the helper leaves messages
// alone when no reload warning was produced.
func TestAppendReloadWarning_Passthrough(t *testing.T) {
	typ, msg := appendReloadWarning("success", "Task saved", "")
	if typ != "success" || msg != "Task saved" {
		t.Errorf("passthrough failed: got (%q, %q)", typ, msg)
	}
}

// TestAppendReloadWarning_ElevatesSuccess verifies a success flash becomes
// a warning and gains the reload failure detail when a warning is present.
func TestAppendReloadWarning_ElevatesSuccess(t *testing.T) {
	typ, msg := appendReloadWarning("success", "Task saved", "scheduler reload failed: bad cron")
	if typ != "warning" {
		t.Errorf("type = %q, want warning", typ)
	}
	if !strings.Contains(msg, "Task saved") || !strings.Contains(msg, "scheduler reload failed") {
		t.Errorf("message missing parts: %q", msg)
	}
}

// TestAppendReloadWarning_PreservesError verifies an error flash is never
// downgraded by a trailing reload warning.
func TestAppendReloadWarning_PreservesError(t *testing.T) {
	typ, msg := appendReloadWarning("error", "Save failed", "scheduler reload failed: bad cron")
	if typ != "error" || msg != "Save failed" {
		t.Errorf("error flash should not be elevated: got (%q, %q)", typ, msg)
	}
}

// TestConfigDefaultsSave_ReloadFailureSurfacesWarning drives a config save
// through the handler with a reloader that fails, and verifies the response
// flash is a warning containing the reload error. Regression test for the
// prior bug where reload errors were silently swallowed.
func TestConfigDefaultsSave_ReloadFailureSurfacesWarning(t *testing.T) {
	s, _ := newTestServer(t)
	reloader, ok := s.reloader.(*mockReloader)
	if !ok {
		t.Fatalf("reloader is not *mockReloader")
	}
	reloader.err = errors.New("invalid cron expression")

	form := url.Values{}
	form.Set("model", "sonnet")
	form.Set("max_turns", "15")
	req := httptest.NewRequest(http.MethodPost, "/web/config/defaults", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleConfigDefaults(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "flash-warning") {
		t.Errorf("expected warning flash, got body: %q", body)
	}
	if !strings.Contains(body, "scheduler reload failed") {
		t.Errorf("warning should describe reload failure, got: %q", body)
	}
	if !strings.Contains(body, "invalid cron expression") {
		t.Errorf("warning should include underlying error, got: %q", body)
	}
}

// TestConfigDefaultsSave_ReloadSuccessNoWarning verifies the happy path —
// when the reloader succeeds, the flash stays a plain success.
func TestConfigDefaultsSave_ReloadSuccessNoWarning(t *testing.T) {
	s, _ := newTestServer(t)

	form := url.Values{}
	form.Set("model", "sonnet")
	form.Set("max_turns", "15")
	req := httptest.NewRequest(http.MethodPost, "/web/config/defaults", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.handleConfigDefaults(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "flash-warning") {
		t.Errorf("unexpected warning flash on success: %q", body)
	}
	if !strings.Contains(body, "flash-success") {
		t.Errorf("expected success flash, got: %q", body)
	}
	if !strings.Contains(body, "Defaults saved") {
		t.Errorf("expected save confirmation, got: %q", body)
	}
}
