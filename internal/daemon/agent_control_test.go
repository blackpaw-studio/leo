package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// The socket's control routes hand the request, path and body intact, to
// the web server's control handler once StartWeb has built it.
func TestSocketControlRoutesDelegateToTheWebServer(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "d.sock"), "", nil)
	var gotPath, gotBody string
	s.setControl(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotPath, gotBody = r.URL.Path, string(b)
		writeJSON(w, http.StatusOK, Response{OK: true, Data: json.RawMessage(`{"transport":"legacy"}`)})
	}))
	for _, verb := range []string{"message", "interrupt", "compact", "clear"} {
		path := "/agents/alpha/" + verb
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(`{"text":"hi"}`)))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"transport":"legacy"`) {
			t.Fatalf("%s: status = %d, body = %s", verb, w.Code, w.Body.String())
		}
		if gotPath != path || gotBody != `{"text":"hi"}` {
			t.Fatalf("%s: delegated (%q, %q)", verb, gotPath, gotBody)
		}
	}
}

// Before the web server exists (or with web disabled) the routes answer 503
// in the socket's envelope.
func TestSocketControlRoutesWithoutTheWebServer(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "d.sock"), "", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/agents/alpha/interrupt", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.OK || resp.Error == "" {
		t.Fatalf("response = %s (%v)", w.Body.String(), err)
	}
}
