package daemon

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestHealthAdvertisesGoroutineDumpOnlyWhenEnabled(t *testing.T) {
	health := func(opts ...Option) string {
		s := New(filepath.Join(t.TempDir(), "leo.sock"), "/nonexistent/leo.yaml", nil, opts...)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
		return w.Body.String()
	}

	if body := health(); strings.Contains(body, "goroutine_dump") {
		t.Errorf("default /health advertises goroutine_dump: %s", body)
	}
	if body := health(WithGoroutineDump()); !strings.Contains(body, `"goroutine_dump":true`) {
		t.Errorf("/health with WithGoroutineDump lacks goroutine_dump:true: %s", body)
	}
}
