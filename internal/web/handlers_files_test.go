package web

import (
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/observe"
)

var filesT0 = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func newFilesTestServer(t *testing.T) (*Server, *observe.SurfacedFileStore) {
	t.Helper()
	s, _, _ := newDeviceTestServer(t)
	store := observe.NewSurfacedFileStore(nil, nil)
	s.surfacedFiles = store
	return s, store
}

func surface(t *testing.T, store *observe.SurfacedFileStore, abs string) string {
	t.Helper()
	f, err := store.Add("assistant", filesT0, observe.SurfaceInput{Path: filepath.Base(abs), AbsPath: abs})
	if err != nil {
		t.Fatal(err)
	}
	return f.ID
}

func fileRequest(s *Server, method, id, bearer string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/api/v1/files/"+id, nil)
	req.Host = testHost
	req.Header.Set("Authorization", "Bearer "+bearer)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestFileRouteServesSurfacedFileByID(t *testing.T) {
	s, store := newFilesTestServer(t)
	path := filepath.Join(t.TempDir(), "notes.md")
	if err := os.WriteFile(path, []byte("hello, phone"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := surface(t, store, path)

	rec := fileRequest(s, http.MethodGet, id, testAPIToken, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "hello, phone" {
		t.Fatalf("GET: status %d body %q", rec.Code, rec.Body)
	}
	if _, params, err := mime.ParseMediaType(rec.Header().Get("Content-Disposition")); err != nil || params["filename"] != "notes.md" {
		t.Fatalf("Content-Disposition = %q (%v)", rec.Header().Get("Content-Disposition"), err)
	}
	// A surfaced HTML file must never run as a page on the daemon's origin.
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Fatalf("CSP = %q, want sandbox", csp)
	}
	if rec.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("Accept-Ranges = %q", rec.Header().Get("Accept-Ranges"))
	}
}

func TestFileRouteSupportsRangeAndHead(t *testing.T) {
	s, store := newFilesTestServer(t)
	path := filepath.Join(t.TempDir(), "data.bin")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := surface(t, store, path)

	rec := fileRequest(s, http.MethodGet, id, testAPIToken, map[string]string{"Range": "bytes=2-5"})
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "2345" {
		t.Fatalf("Range: status %d body %q", rec.Code, rec.Body)
	}
	if cr := rec.Header().Get("Content-Range"); cr != "bytes 2-5/10" {
		t.Fatalf("Content-Range = %q", cr)
	}

	rec = fileRequest(s, http.MethodHead, id, testAPIToken, nil)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "10" {
		t.Fatalf("HEAD: status %d len %d Content-Length %q", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Length"))
	}
}

func TestFileRouteNotFoundWhenUnknownOrEvicted(t *testing.T) {
	s, store := newFilesTestServer(t)
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := surface(t, store, path)

	if rec := fileRequest(s, http.MethodGet, "not-an-id", testAPIToken, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: status %d", rec.Code)
	}
	store.Reset("assistant", filesT0.Add(time.Minute))
	if rec := fileRequest(s, http.MethodGet, id, testAPIToken, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("evicted id: status %d", rec.Code)
	}

	gone, err := store.Add("other", filesT0, observe.SurfaceInput{Path: "deleted.txt", AbsPath: filepath.Join(t.TempDir(), "deleted.txt")})
	if err != nil {
		t.Fatal(err)
	}
	if rec := fileRequest(s, http.MethodGet, gone.ID, testAPIToken, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("deleted file: status %d", rec.Code)
	}
}

func TestFileRouteNeverTakesAPath(t *testing.T) {
	s, store := newFilesTestServer(t)
	surface(t, store, "/etc/hosts")
	for _, p := range []string{"/api/v1/files/%2Fetc%2Fhosts", "/api/v1/files/..%2F..%2Fetc%2Fhosts", "/api/v1/files/?path=/etc/hosts"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Host = testHost
		req.Header.Set("Authorization", "Bearer "+testAPIToken)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			t.Fatalf("%s served a file: %q", p, rec.Body)
		}
	}
}

func TestFileRouteRefusesNonRegularFiles(t *testing.T) {
	s, store := newFilesTestServer(t)
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	// A file swapped for a directory or FIFO after it was surfaced.
	for name, path := range map[string]string{"directory": dir, "fifo": fifo} {
		id := surface(t, store, path)
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- fileRequest(s, http.MethodGet, id, testAPIToken, nil) }()
		select {
		case rec := <-done:
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s: status %d, want 403", name, rec.Code)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: request hung (opened without O_NONBLOCK?)", name)
		}
	}
}

func TestFileRouteIsOperatorOnly(t *testing.T) {
	s, store := newFilesTestServer(t)
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := surface(t, store, path)
	if rec := fileRequest(s, http.MethodGet, id, "agent-token", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("agent token: status %d, want 403", rec.Code)
	}
	if rec := fileRequest(s, http.MethodGet, id, "nope", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token: status %d, want 401", rec.Code)
	}
	body, _ := io.ReadAll(fileRequest(s, http.MethodGet, id, testAPIToken, nil).Body)
	if string(body) != "a" {
		t.Fatalf("operator token: body %q", body)
	}
}
