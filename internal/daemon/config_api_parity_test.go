package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// parityServer is a real daemon with its web server started: the socket's
// Handler() and the web listener's /api/v1 handler, with the operator token.
type parityServer struct {
	t       *testing.T
	socket  http.Handler
	web     http.Handler
	token   string
	host    string
	cfgPath string
}

func newParityServer(t *testing.T) *parityServer {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")
	port := freeTCPPort(t)
	yaml := fmt.Sprintf("defaults:\n  model: sonnet\nweb:\n  enabled: true\n  port: %d\ntemplates:\n  coder:\n    model: opus\ntasks:\n  heartbeat:\n    schedule: \"0 * * * *\"\n    prompt_file: heartbeat.md\n", port)
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o750); err != nil {
		t.Fatal(err)
	}
	s := New(tmpSockPath(t, "p.sock"), cfgPath, nil)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.StartWeb(cfg, nil); err != nil {
		t.Fatalf("StartWeb: %v", err)
	}
	t.Cleanup(func() { _ = s.webServer.Shutdown() })
	token, err := os.ReadFile(filepath.Join(dir, "state", "api.token"))
	if err != nil {
		t.Fatal(err)
	}
	return &parityServer{t: t, socket: s.Handler(), web: s.webServer.Handler(),
		token: strings.TrimSpace(string(token)), host: fmt.Sprintf("127.0.0.1:%d", port), cfgPath: cfgPath}
}

// do sends the same request to the socket handler (path as is) and to the web
// handler (under /api/v1, with the operator token).
func (p *parityServer) do(method, path, ifMatch string, body any) (socket, web *httptest.ResponseRecorder) {
	p.t.Helper()
	send := func(h http.Handler, prefix string, authed bool) *httptest.ResponseRecorder {
		var rdr *bytes.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			rdr = bytes.NewReader(raw)
		} else {
			rdr = bytes.NewReader(nil)
		}
		req := httptest.NewRequest(method, prefix+path, rdr)
		req.Host = p.host
		if authed {
			req.Header.Set("Authorization", "Bearer "+p.token)
		}
		if ifMatch != "" {
			req.Header.Set("If-Match", ifMatch)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	return send(p.socket, "", false), send(p.web, "/api/v1", true)
}

func (p *parityServer) revision() string {
	raw, err := os.ReadFile(p.cfgPath)
	if err != nil {
		p.t.Fatal(err)
	}
	return config.RevisionOf(raw)
}

// The socket and the web API serve one service: the same request through the
// real daemon socket handler and the real authenticated web handler gets the
// same status and the same bytes, for reads, a refused write and an accepted
// one (which the other side then sees).
func TestSocketAndWebAPIServeIdenticalBodies(t *testing.T) {
	p := newParityServer(t)

	for _, path := range []string{
		"/config/schema", "/config/defaults", "/config/templates", "/config/templates/coder",
		"/config/tasks/heartbeat", "/config/web", "/config/delegation", "/config/nope",
		"/tasks/heartbeat/history", "/service/status", "/cron/preview?expr=0+*+*+*+*",
	} {
		a, b := p.do(http.MethodGet, path, "", nil)
		if a.Code != b.Code || a.Body.String() != b.Body.String() {
			t.Errorf("GET %s differs:\n socket %d %s\n web    %d %s", path, a.Code, a.Body, b.Code, b.Body)
		}
	}

	stale := map[string]any{"values": map[string]any{"model": "haiku"}}
	a, b := p.do(http.MethodPut, "/config/defaults", "sha256:stale", stale)
	if a.Code != http.StatusConflict || a.Code != b.Code || a.Body.String() != b.Body.String() {
		t.Errorf("409 differs:\n socket %d %s\n web    %d %s", a.Code, a.Body, b.Code, b.Body)
	}

	// A write through one side is what the other reads next.
	a, _ = p.do(http.MethodPut, "/config/defaults", p.revision(), map[string]any{"values": map[string]any{"model": "opus"}})
	if a.Code != http.StatusOK {
		t.Fatalf("socket write = %d %s", a.Code, a.Body)
	}
	a, b = p.do(http.MethodGet, "/config/defaults", "", nil)
	if a.Body.String() != b.Body.String() || !strings.Contains(b.Body.String(), `"model":"opus"`) {
		t.Errorf("web does not see the socket's write:\n socket %s\n web    %s", a.Body, b.Body)
	}
}
