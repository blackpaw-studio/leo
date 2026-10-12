package daemon

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// The socket serves Leo Chat at the same /api/v1/chat paths as TCP, by
// handing each request to the web server's chat handler.
func TestSocketChatRoutesDelegateToTheWebServer(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "d.sock"), "", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/chat/threads", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("before the web server: status = %d", w.Code)
	}

	var got []string
	s.setChat(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}))
	for _, req := range []*http.Request{
		httptest.NewRequest("GET", "/api/v1/chat/threads", nil),
		httptest.NewRequest("POST", "/api/v1/chat/assistant/messages", nil),
		httptest.NewRequest("GET", "/api/v1/chat/attachments/01X", nil),
	} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusTeapot {
			t.Fatalf("%s %s: status = %d", req.Method, req.URL.Path, w.Code)
		}
	}
	if len(got) != 3 || got[1] != "POST /api/v1/chat/assistant/messages" {
		t.Fatalf("delegated %v", got)
	}
}
