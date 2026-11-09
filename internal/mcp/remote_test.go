package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		in         string
		wantAgent  string
		wantHost   string
		wantRemote bool
		wantErr    string
	}{
		{in: "rocket", wantAgent: "rocket"},
		{in: "dispatch:abc123", wantAgent: "dispatch:abc123"},
		{in: "rocket@laptop", wantAgent: "rocket", wantHost: "laptop", wantRemote: true},
		{in: "a-b.c_d@my-host", wantAgent: "a-b.c_d", wantHost: "my-host", wantRemote: true},
		{in: "@laptop", wantErr: "agent name"},
		{in: "rocket@", wantErr: "host name"},
		{in: "@", wantErr: "agent name"},
		{in: "a@b@c", wantErr: "exactly one '@'"},
		{in: "rocket@lap top", wantErr: "whitespace"},
		{in: " rocket@laptop", wantErr: "whitespace"},
		{in: "rocket@laptop\n", wantErr: "whitespace"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			agent, host, remote, err := parseTarget(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseTarget(%q) err = %v, want containing %q", tc.in, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTarget(%q) unexpected error: %v", tc.in, err)
			}
			if agent != tc.wantAgent || host != tc.wantHost || remote != tc.wantRemote {
				t.Fatalf("parseTarget(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.in, agent, host, remote, tc.wantAgent, tc.wantHost, tc.wantRemote)
			}
		})
	}
}

const peerToken = "peer-secret-7e2b"

type peerCall struct {
	Method, Path, Auth, ContentType, Body string
}

// fakePeer stands in for the receiving host's web listener.
type fakePeer struct {
	srv     *httptest.Server
	mu      sync.Mutex
	calls   []peerCall
	respond func(w http.ResponseWriter, r *http.Request)
}

func newFakePeer(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) *fakePeer {
	t.Helper()
	p := &fakePeer{respond: respond}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.calls = append(p.calls, peerCall{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), string(body)})
		p.mu.Unlock()
		p.respond(w, r)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakePeer) recorded() []peerCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]peerCall(nil), p.calls...)
}

func peerOK(w http.ResponseWriter, _ *http.Request) {
	w.Write([]byte(`{"ok":true,"data":{"transport":"tmux"}}`))
}

func peerStatus(status int, errText string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": errText})
	}
}

// remoteHostDaemon is the local daemon: it serves only the remote-host route
// and fails the test on any other request (a remote send must not touch the
// local message route).
func remoteHostDaemon(t *testing.T, apiURL string) *fakeDaemon {
	t.Helper()
	d := newFakeDaemon(func(method, path string, _ []byte) (int, string) {
		switch {
		case method == "GET" && path == "/api/remote-host/laptop":
			b, _ := json.Marshal(map[string]any{"ok": true, "data": map[string]string{"api_url": apiURL, "api_token": peerToken, "api_client": "dionysus"}})
			return 200, string(b)
		case path == "/api/remote-host/nope":
			return 404, `{"ok":false,"error":"unknown host \"nope\" (not in client.hosts)"}`
		}
		t.Errorf("unexpected local daemon request %s %s", method, path)
		return 500, `{"ok":false,"error":"unexpected"}`
	})
	t.Cleanup(d.close)
	return d
}

func remoteRegistry(d *fakeDaemon, perms leotools.Permissions, opts ...registryOption) *registry {
	return newRegistry(newDaemonClient(d.port(), "agent-token"), "scout", perms, opts...)
}

func TestSendMessage_Remote_RequestShape(t *testing.T) {
	peer := newFakePeer(t, peerOK)
	reg := remoteRegistry(remoteHostDaemon(t, peer.srv.URL), leotools.Permissions{})

	out, err := callTool(reg, "leo_send_message", map[string]any{"to": "rocket@laptop", "message": "hi\nthere"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if out != "Sent message to rocket@laptop" {
		t.Errorf("result = %q", out)
	}
	calls := peer.recorded()
	if len(calls) != 1 {
		t.Fatalf("peer got %d calls, want 1: %+v", len(calls), calls)
	}
	want := peerCall{
		Method:      "POST",
		Path:        "/web/agent/rocket/message",
		Auth:        "Bearer " + peerToken,
		ContentType: "application/json",
		Body:        `{"from":"dionysus#scout","text":"hi\nthere"}`,
	}
	if calls[0] != want {
		t.Errorf("peer call:\n got %+v\nwant %+v", calls[0], want)
	}
}

func TestSendMessage_Remote_AgentNameIsPathEscaped(t *testing.T) {
	peer := newFakePeer(t, peerOK)
	reg := remoteRegistry(remoteHostDaemon(t, peer.srv.URL), leotools.Permissions{})
	if _, err := callTool(reg, "leo_send_message", map[string]any{"to": "a/b?x#y@laptop", "message": "hi"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := peer.recorded(); len(got) != 1 || got[0].Path != "/web/agent/a/b?x#y/message" {
		// r.URL.Path is the decoded form; the escaped one must not have
		// split the path or smuggled a query.
		t.Fatalf("calls = %+v", got)
	}
}

func TestSendMessage_Remote_PermissionsCheckFullTarget(t *testing.T) {
	peer := newFakePeer(t, peerOK)
	d := remoteHostDaemon(t, peer.srv.URL)

	// A local glob must not authorize a remote target, and an explicit
	// remote entry must not authorize another agent on that host.
	for _, can := range [][]string{{"*"}, {"rocket"}, {"infra@laptop"}} {
		reg := remoteRegistry(d, leotools.Permissions{CanMessage: can})
		_, err := callTool(reg, "leo_send_message", map[string]any{"to": "rocket@laptop", "message": "hi"})
		if err == nil || !strings.Contains(err.Error(), "not permitted") {
			t.Errorf("can_message %v: err = %v, want a permission denial", can, err)
		}
	}
	if len(peer.recorded()) != 0 || len(d.calls) != 0 {
		t.Fatalf("a denied send reached the network: peer=%v local=%v", peer.recorded(), d.calls)
	}

	reg := remoteRegistry(d, leotools.Permissions{CanMessage: []string{"rocket@laptop"}})
	if _, err := callTool(reg, "leo_send_message", map[string]any{"to": "rocket@laptop", "message": "hi"}); err != nil {
		t.Fatalf("allowed remote target rejected: %v", err)
	}
}

func TestSendMessage_Remote_DispatchChildRefused(t *testing.T) {
	peer := newFakePeer(t, peerOK)
	d := remoteHostDaemon(t, peer.srv.URL)
	reg := newRegistry(newDaemonClient(d.port(), "tok"), "dispatch:d-9", leotools.Permissions{}, withDispatchID("d-9"))
	_, err := callTool(reg, "leo_send_message", map[string]any{"to": "rocket@laptop", "message": "hi"})
	if err == nil || !strings.Contains(err.Error(), "leo_dispatch subagent") {
		t.Fatalf("err = %v", err)
	}
	if len(peer.recorded()) != 0 || len(d.calls) != 0 {
		t.Fatal("a refused dispatch child reached the network")
	}
}

func TestSendMessage_Remote_MalformedTargetsNeverReachNetwork(t *testing.T) {
	peer := newFakePeer(t, peerOK)
	d := remoteHostDaemon(t, peer.srv.URL)
	reg := remoteRegistry(d, leotools.Permissions{})
	for _, to := range []string{"@laptop", "rocket@", "a@b@c"} {
		if _, err := callTool(reg, "leo_send_message", map[string]any{"to": to, "message": "hi"}); err == nil {
			t.Errorf("to=%q: expected an error", to)
		}
	}
	if len(peer.recorded()) != 0 || len(d.calls) != 0 {
		t.Fatal("a malformed target reached the network")
	}
}

func TestSendMessage_Remote_UnknownHostNamesIt(t *testing.T) {
	d := remoteHostDaemon(t, "http://unused.invalid")
	reg := remoteRegistry(d, leotools.Permissions{})
	_, err := callTool(reg, "leo_send_message", map[string]any{"to": "rocket@nope", "message": "hi"})
	if err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestSendMessage_Remote_PeerErrorsNeverLeakToken(t *testing.T) {
	// A hostile or buggy peer that echoes the Authorization header back.
	peer := newFakePeer(t, peerStatus(403, "client dionysus lacks access; presented "+peerToken))
	reg := remoteRegistry(remoteHostDaemon(t, peer.srv.URL), leotools.Permissions{})
	_, err := callTool(reg, "leo_send_message", map[string]any{"to": "infra@laptop", "message": "hi"})
	if err == nil || !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), peerToken) {
		t.Fatalf("error leaks the token: %v", err)
	}
}

func TestSendMessage_Remote_RedirectIsNotFollowed(t *testing.T) {
	other := newFakePeer(t, peerOK)
	peer := newFakePeer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.srv.URL+r.URL.Path, http.StatusTemporaryRedirect)
	})
	reg := remoteRegistry(remoteHostDaemon(t, peer.srv.URL), leotools.Permissions{})
	_, err := callTool(reg, "leo_send_message", map[string]any{"to": "rocket@laptop", "message": "hi"})
	if err == nil || !strings.Contains(err.Error(), "redirected") {
		t.Fatalf("err = %v", err)
	}
	if got := other.recorded(); len(got) != 0 {
		t.Fatalf("the redirect target received a request (token leak path): %+v", got)
	}
}

func TestSendMessage_Remote_Unreachable(t *testing.T) {
	peer := newFakePeer(t, peerOK)
	url := peer.srv.URL
	peer.srv.Close()
	reg := remoteRegistry(remoteHostDaemon(t, url), leotools.Permissions{})
	_, err := callTool(reg, "leo_send_message", map[string]any{"to": "rocket@laptop", "message": "hi"})
	if err == nil || !strings.Contains(err.Error(), "host laptop unreachable at "+url) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), peerToken) {
		t.Fatalf("error leaks the token: %v", err)
	}
}

func TestSendMessage_Remote_TimeoutIsReported(t *testing.T) {
	release := make(chan struct{})
	peer := newFakePeer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })
	rc := &remoteClient{http: &http.Client{Timeout: 50 * time.Millisecond}}
	reg := remoteRegistry(remoteHostDaemon(t, peer.srv.URL), leotools.Permissions{}, withRemote(rc))
	_, err := callTool(reg, "leo_send_message", map[string]any{"to": "rocket@laptop", "message": "hi"})
	if err == nil || !strings.Contains(err.Error(), "did not answer within 50ms") {
		t.Fatalf("err = %v", err)
	}
}

func TestMapRemoteError(t *testing.T) {
	h := remoteHost{Name: "laptop", URL: "http://10.0.4.7:8370", Token: peerToken, ClientName: "dionysus"}
	body := func(msg string) []byte { b, _ := json.Marshal(map[string]string{"error": msg}); return b }
	cases := []struct {
		name    string
		agent   string
		status  int
		body    []byte
		err     error
		want    string
		notWant string
	}{
		{"401", "rocket", 401, body("unauthorized"), nil, "host laptop rejected the api_token", ""},
		{"403 passes the receiver's reason", "rocket", 403, body(`client "dionysus" is not permitted to message "rocket"`), nil, `not permitted to message rocket on laptop: client "dionysus" is not permitted`, ""},
		{"403 without a body", "rocket", 403, nil, nil, "not permitted to message rocket on laptop", ""},
		{"404 with an agent", "rocket", 404, body("x"), nil, "no agent rocket on laptop", ""},
		{"404 on discovery", "", 404, nil, nil, "does not serve this request", ""},
		{"400 passes the message", "rocket", 400, body("text is required"), nil, "host laptop rejected the request: text is required", ""},
		{"500", "rocket", 500, body("boom"), nil, "host laptop returned status 500: boom", ""},
		{"502 without a body", "rocket", 502, []byte("<html>"), nil, "host laptop returned status 502: no detail", ""},
		{"3xx", "rocket", 307, nil, nil, "redirected the request (status 307)", ""},
		{"connection refused", "rocket", 0, nil, &url.Error{Op: "Post", URL: h.URL + "?t=" + peerToken, Err: syscall.ECONNREFUSED}, "host laptop unreachable at http://10.0.4.7:8370", peerToken},
		{"timeout", "rocket", 0, nil, &url.Error{Op: "Post", URL: h.URL, Err: context.DeadlineExceeded}, "did not answer within 10s", ""},
		{"token echoed by the peer is redacted", "rocket", 403, body("saw " + peerToken), nil, "saw [redacted]", peerToken},
		{"long peer text is bounded", "rocket", 500, body(strings.Repeat("x", 500)), nil, strings.Repeat("x", 200) + "…", strings.Repeat("x", 201)},
		{"control characters are stripped", "rocket", 500, body("a\nb\x1b[31mc"), nil, "a b [31mc", "\x1b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := mapRemoteError(h, tc.agent, tc.status, tc.body, tc.err, remoteTimeout)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
			if tc.notWant != "" && strings.Contains(err.Error(), tc.notWant) {
				t.Fatalf("err = %v, must not contain %q", err, tc.notWant)
			}
		})
	}
}

func TestListAgents_RemoteHost(t *testing.T) {
	const listing = `[{"name":"rocket","status":"running"}]`
	peer := newFakePeer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"ok":true,"data":` + listing + `}`))
	})
	d := remoteHostDaemon(t, peer.srv.URL)
	reg := remoteRegistry(d, leotools.Permissions{})

	out, err := callTool(reg, "leo_list_agents", map[string]any{"host": "laptop"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if out != listing {
		t.Errorf("result = %q, want %q", out, listing)
	}
	calls := peer.recorded()
	want := peerCall{Method: "GET", Path: "/web/client/agents", Auth: "Bearer " + peerToken}
	if len(calls) != 1 || calls[0] != want {
		t.Fatalf("peer calls = %+v, want [%+v]", calls, want)
	}
}

func TestListAgents_RemoteHostErrors(t *testing.T) {
	t.Run("old peer without the route", func(t *testing.T) {
		peer := newFakePeer(t, peerStatus(404, "not found"))
		reg := remoteRegistry(remoteHostDaemon(t, peer.srv.URL), leotools.Permissions{})
		_, err := callTool(reg, "leo_list_agents", map[string]any{"host": "laptop"})
		if err == nil || !strings.Contains(err.Error(), "does not serve this request") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("wrong token", func(t *testing.T) {
		peer := newFakePeer(t, peerStatus(401, "unauthorized"))
		reg := remoteRegistry(remoteHostDaemon(t, peer.srv.URL), leotools.Permissions{})
		_, err := callTool(reg, "leo_list_agents", map[string]any{"host": "laptop"})
		if err == nil || !strings.Contains(err.Error(), "rejected the api_token") || strings.Contains(err.Error(), peerToken) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unknown host", func(t *testing.T) {
		reg := remoteRegistry(remoteHostDaemon(t, "http://unused.invalid"), leotools.Permissions{})
		_, err := callTool(reg, "leo_list_agents", map[string]any{"host": "nope"})
		if err == nil || !strings.Contains(err.Error(), `"nope"`) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("dispatch child refused", func(t *testing.T) {
		d := remoteHostDaemon(t, "http://unused.invalid")
		reg := newRegistry(newDaemonClient(d.port(), "tok"), "dispatch:d-1", leotools.Permissions{}, withDispatchID("d-1"))
		if _, err := callTool(reg, "leo_list_agents", map[string]any{"host": "laptop"}); err == nil {
			t.Fatal("expected a refusal")
		}
	})
}

func TestListAgents_NoHostStaysLocal(t *testing.T) {
	d := newFakeDaemon(func(method, path string, _ []byte) (int, string) {
		if method != "GET" || path != "/api/agent/list" {
			t.Errorf("unexpected request %s %s", method, path)
		}
		return 200, `{"ok":true,"data":[{"name":"local-1"}]}`
	})
	defer d.close()
	reg := newRegistry(newDaemonClient(d.port(), "tok"), "scout", leotools.Permissions{})
	for _, args := range []map[string]any{{}, {"host": ""}} {
		if out, err := callTool(reg, "leo_list_agents", args); err != nil || out != `[{"name":"local-1"}]` {
			t.Fatalf("args %v: out=%q err=%v", args, out, err)
		}
	}
}
