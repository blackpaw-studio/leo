package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/peercred"
)

// The context relay reaches the daemon over its unix socket, where the kernel
// names the process that connected; the web server's handler decides from it.
// Until StartWeb has built that server the route answers 503, as the other
// delegated socket routes do.
func TestSocketAgentContextRouteDelegatesToTheWebServer(t *testing.T) {
	s := New(tmpSockPath(t, "d.sock"), "", nil)
	status := func() int {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest("POST", "/api/agent/context", strings.NewReader(`{"token":"t"}`)))
		res := w.Result()
		defer func() { _ = res.Body.Close() }()
		return res.StatusCode
	}
	if got := status(); got != http.StatusServiceUnavailable {
		t.Fatalf("before StartWeb: status = %d, want 503", got)
	}

	var gotBody string
	s.setAgentContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 64)
		n, _ := r.Body.Read(b)
		gotBody = string(b[:n])
		_, _ = w.Write([]byte(`{"context":"x"}`))
	}))
	if got := status(); got != http.StatusOK || gotBody != `{"token":"t"}` {
		t.Fatalf("after StartWeb: status = %d, delegated body %q", got, gotBody)
	}
}

// Over the real socket the request context carries the connecting process as
// the kernel recorded it: here the test process, which dialed.
func TestSocketRequestsCarryTheKernelVerifiedPeerPID(t *testing.T) {
	sockPath := tmpSockPath(t, "d.sock")
	s := New(sockPath, "/tmp/leo.yaml", nil)
	var gotPID int
	var gotOK bool
	s.setAgentContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPID, gotOK = peercred.VerifiedPID(r.Context())
		_, _ = w.Write([]byte(`{"context":""}`))
	}))
	if err := s.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = s.Shutdown() }()

	resp, err := socketHTTPClient(sockPath).Post("http://localhost/api/agent/context", "application/json", strings.NewReader(`{"token":"t","pid":`+strconv.Itoa(1)+`}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if !gotOK || gotPID != os.Getpid() {
		t.Fatalf("peer = %d, %v; want this process %d (the body claimed pid 1)", gotPID, gotOK, os.Getpid())
	}
}
