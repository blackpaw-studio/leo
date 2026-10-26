package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/clienttools"
	"github.com/blackpaw-studio/leo/internal/leotools"
)

const ctObjectSchema = `{"type":"object","properties":{"ref":{"type":"string"}},"required":["ref"]}`

func ctTool(name string, timeoutS int) map[string]any {
	return map[string]any{"name": name, "description": "does " + name, "input_schema": json.RawMessage(ctObjectSchema), "timeout_s": timeoutS}
}

type clientToolsServer struct {
	s     *Server
	relay *clienttools.Relay
}

func newClientToolsServer(t *testing.T) clientToolsServer {
	t.Helper()
	s := newTokenSplitServer(t)
	relay := clienttools.NewRelay(clienttools.RelayOptions{
		Registry: clienttools.RegistryOptions{IsBuiltin: leotools.IsKnownTool},
		Flag:     clienttools.NewEverRegistered(clienttools.NewFileFlagStore(filepath.Join(t.TempDir(), "client-tools.json")), nil),
	})
	s.clientTools = relay
	return clientToolsServer{s: s, relay: relay}
}

func (c clientToolsServer) do(t *testing.T, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw string
	switch v := body.(type) {
	case nil:
	case string:
		raw = v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		raw = string(b)
	}
	return chatRequest(t, c.s, method, path, token, raw)
}

func (c clientToolsServer) register(t *testing.T, clientID, group string, tools ...map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return c.do(t, http.MethodPost, "/api/v1/client-tools", testAPIToken, map[string]any{"client_id": clientID, "group": group, "tools": tools})
}

func jsonEqualToFixture(t *testing.T, name string, got []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "clienttools", "testdata", "wire", name))
	if err != nil {
		t.Fatal(err)
	}
	var want, have any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &have); err != nil {
		t.Fatalf("%s: %v (%s)", name, err, got)
	}
	if !reflect.DeepEqual(have, want) {
		t.Errorf("%s:\n got %s\nwant %s", name, got, raw)
	}
}

func TestClientToolsRoutesStatusCodes(t *testing.T) {
	c := newClientToolsServer(t)
	id := string(c.relay.Connect())

	t.Run("register ok", func(t *testing.T) {
		w := c.register(t, id, "browser", ctTool("browser_click", 30))
		if w.Code != http.StatusOK {
			t.Fatalf("status %d %s", w.Code, w.Body)
		}
		var env struct{ Data struct{ Revision uint64 } }
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || env.Data.Revision != 1 {
			t.Fatalf("body %s", w.Body)
		}
	})
	for _, tc := range []struct {
		name    string
		body    any
		status  int
		fixture string
	}{
		{"invalid tool", map[string]any{"client_id": id, "group": "browser", "tools": []any{ctTool("Bad Name", 30)}}, 400, "register-error-invalid-tool.json"},
		{"unknown client", map[string]any{"client_id": "ct_nope", "group": "browser", "tools": []any{ctTool("browser_click", 30)}}, 404, "register-error-unknown-client.json"},
		{"builtin collision", map[string]any{"client_id": id, "group": "other", "tools": []any{ctTool("leo_chat_post", 30)}}, 409, "register-error-name-conflict.json"},
		{"too many tools", map[string]any{"client_id": id, "group": "big", "tools": manyCTTools(65)}, 400, "register-error-too-many-tools.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := c.do(t, http.MethodPost, "/api/v1/client-tools", testAPIToken, tc.body)
			if w.Code != tc.status {
				t.Fatalf("status %d, want %d (%s)", w.Code, tc.status, w.Body)
			}
			jsonEqualToFixture(t, tc.fixture, w.Body.Bytes())
		})
	}
	t.Run("malformed body", func(t *testing.T) {
		if w := c.do(t, http.MethodPost, "/api/v1/client-tools", testAPIToken, "{not json"); w.Code != http.StatusBadRequest {
			t.Fatalf("status %d", w.Code)
		}
	})
	t.Run("list", func(t *testing.T) {
		w := c.do(t, http.MethodGet, "/api/v1/client-tools", testAPIToken, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("status %d %s", w.Code, w.Body)
		}
		var env struct {
			Data clienttools.ListData `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		if !env.Data.EverRegistered || len(env.Data.Tools) != 1 || env.Data.Tools[0].Name != "browser_click" {
			t.Fatalf("list = %+v", env.Data)
		}
	})
}

func manyCTTools(n int) []map[string]any {
	out := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, ctTool(fmt.Sprintf("t%d", i), 30))
	}
	return out
}

// callAsAgent parks a call through the agent route and returns its recorder.
func (c clientToolsServer) callAsAgent(t *testing.T, agent, tool string) <-chan *httptest.ResponseRecorder {
	t.Helper()
	out := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		out <- c.do(t, http.MethodPost, "/api/agent/"+agent+"/client-tools/call", testAgentToken, map[string]any{"tool": tool, "args": map[string]any{"ref": "e12"}})
	}()
	return out
}

func (c clientToolsServer) firstPending(t *testing.T) clienttools.PendingRequest {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if p := c.relay.Pending(); len(p) > 0 {
			return p[0]
		}
		select {
		case <-deadline:
			t.Fatal("no request was parked")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestClientToolsClaimResultAndCallLifecycle(t *testing.T) {
	c := newClientToolsServer(t)
	a, b := string(c.relay.Connect()), string(c.relay.Connect())
	for _, id := range []string{a, b} {
		if w := c.register(t, id, "browser", ctTool("browser_click", 30)); w.Code != http.StatusOK {
			t.Fatalf("register: %d %s", w.Code, w.Body)
		}
	}

	called := c.callAsAgent(t, "alfred", "browser_click")
	req := c.firstPending(t)
	reqPath := "/api/v1/client-tools/requests/" + req.ID

	if w := c.do(t, http.MethodPost, reqPath+"/claim", testAPIToken, map[string]string{"client_id": a}); w.Code != http.StatusOK {
		t.Fatalf("claim: %d %s", w.Code, w.Body)
	}
	w := c.do(t, http.MethodPost, reqPath+"/claim", testAPIToken, map[string]string{"client_id": b})
	if w.Code != http.StatusConflict {
		t.Fatalf("second claim: %d %s", w.Code, w.Body)
	}
	jsonEqualToFixtureCode(t, w.Body.Bytes(), "already_claimed")
	if w := c.do(t, http.MethodPost, "/api/v1/client-tools/requests/ctr_nope/claim", testAPIToken, map[string]string{"client_id": a}); w.Code != http.StatusNotFound {
		t.Fatalf("claim unknown: %d", w.Code)
	} else {
		jsonEqualToFixture(t, "claim-404-unknown-request.json", w.Body.Bytes())
	}

	pending := c.do(t, http.MethodGet, "/api/v1/client-tools/requests?pending=1", testAPIToken, nil)
	var penv struct {
		Data struct {
			Requests []map[string]any `json:"requests"`
		} `json:"data"`
	}
	if err := json.Unmarshal(pending.Body.Bytes(), &penv); err != nil || len(penv.Data.Requests) != 1 || penv.Data.Requests[0]["claimed_by"] != a {
		t.Fatalf("pending = %s", pending.Body)
	}

	if w := c.do(t, http.MethodPost, reqPath+"/result", testAPIToken, map[string]any{"client_id": b, "ok": true, "content": []any{}}); w.Code != http.StatusConflict {
		t.Fatalf("result by non-claimant: %d", w.Code)
	} else {
		jsonEqualToFixture(t, "result-409-not-claimant.json", w.Body.Bytes())
	}
	// The raw content blocks (an image here) reach the agent unchanged.
	okBody := `{"client_id":"` + a + `","ok":true,"content":[{"type":"text","text":"clicked e12"},{"type":"image","data":"iVBORw0KGgo=","mimeType":"image/png"}]}`
	if w := c.do(t, http.MethodPost, reqPath+"/result", testAPIToken, okBody); w.Code != http.StatusOK {
		t.Fatalf("result: %d %s", w.Code, w.Body)
	} else {
		jsonEqualToFixture(t, "result-response.json", w.Body.Bytes())
	}
	w = c.do(t, http.MethodPost, reqPath+"/result", testAPIToken, okBody)
	if w.Code != http.StatusConflict {
		t.Fatalf("second result: %d", w.Code)
	}
	jsonEqualToFixture(t, "result-409-already-settled.json", w.Body.Bytes())

	got := <-called
	if got.Code != http.StatusOK {
		t.Fatalf("call: %d %s", got.Code, got.Body)
	}
	var cenv struct {
		OK   bool `json:"ok"`
		Data struct {
			OK      bool            `json:"ok"`
			Content json.RawMessage `json:"content"`
		} `json:"data"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &cenv); err != nil || !cenv.OK || !cenv.Data.OK {
		t.Fatalf("call body = %s", got.Body)
	}
	if !bytes.Contains(got.Body.Bytes(), []byte(`"mimeType":"image/png"`)) {
		t.Fatalf("image block lost: %s", got.Body)
	}
}

func jsonEqualToFixtureCode(t *testing.T, body []byte, code string) {
	t.Helper()
	var env struct{ Code string }
	if err := json.Unmarshal(body, &env); err != nil || env.Code != code {
		t.Fatalf("body %s, want code %q", body, code)
	}
}

func TestClientToolsCallRoute(t *testing.T) {
	c := newClientToolsServer(t)
	if w := c.do(t, http.MethodPost, "/api/agent/alfred/client-tools/call", testAgentToken, map[string]any{"tool": "nope"}); w.Code != http.StatusNotFound {
		t.Fatalf("unknown tool: %d %s", w.Code, w.Body)
	}
	id := string(c.relay.Connect())
	c.register(t, id, "browser", ctTool("browser_click", 30))
	c.relay.Disconnect(clienttools.ClientID(id))
	w := c.do(t, http.MethodPost, "/api/agent/alfred/client-tools/call", testAgentToken, map[string]any{"tool": "browser_click"})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"code":"no_client"`) {
		t.Fatalf("withdrawn tool: %d %s", w.Code, w.Body)
	}
}

func TestClientToolsAgentListRoute(t *testing.T) {
	c := newClientToolsServer(t)
	id := string(c.relay.Connect())
	c.register(t, id, "browser", ctTool("browser_click", 30))
	w := c.do(t, http.MethodGet, "/api/agent/alfred/client-tools", testAgentToken, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"browser_click"`) {
		t.Fatalf("agent list: %d %s", w.Code, w.Body)
	}
}

func TestAgentTokenCannotOpenProviderStream(t *testing.T) {
	c := newClientToolsServer(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/events?client_tools=1"},
		{http.MethodPost, "/api/v1/client-tools"},
		{http.MethodGet, "/api/v1/client-tools"},
		{http.MethodGet, "/api/v1/client-tools/requests?pending=1"},
		{http.MethodPost, "/api/v1/client-tools/requests/ctr_1/claim"},
		{http.MethodPost, "/api/v1/client-tools/requests/ctr_1/result"},
	} {
		if w := c.do(t, tc.method, tc.path, testAgentToken, "{}"); w.Code != http.StatusForbidden {
			t.Errorf("%s %s with the agent token: %d, want 403", tc.method, tc.path, w.Code)
		}
		if w := c.do(t, tc.method, tc.path, "", "{}"); w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a token: %d, want 401", tc.method, tc.path, w.Code)
		}
	}
}

func TestOperatorProviderStreamGetsClientID(t *testing.T) {
	c := newClientToolsServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events?client_tools=1", nil).WithContext(ctx)
	req.Host = testHost
	req.Header.Set("Authorization", "Bearer "+testAPIToken)
	w := &flushRecorder{ResponseRecorder: httptest.NewRecorder(), flushed: make(chan struct{}, 4)}
	done := make(chan struct{})
	go func() {
		c.s.httpServer.Handler.ServeHTTP(w, req)
		close(done)
	}()
	select {
	case <-w.flushed:
	case <-time.After(5 * time.Second):
		t.Fatal("no hello")
	}
	cancel()
	<-done
	if !strings.Contains(w.Body.String(), `"client_id":"ct_`) {
		t.Fatalf("hello = %q", w.Body.String())
	}
}

type flushRecorder struct {
	*httptest.ResponseRecorder
	flushed chan struct{}
}

func (f *flushRecorder) Flush() {
	f.ResponseRecorder.Flush()
	select {
	case f.flushed <- struct{}{}:
	default:
	}
}

// The call route blocks for as long as the app takes, past the server's
// WriteTimeout; its write deadline must be lifted or the answer is lost.
func TestCallRouteDisablesWriteDeadline(t *testing.T) {
	c := newClientToolsServer(t)
	id := string(c.relay.Connect())
	if w := c.register(t, id, "browser", ctTool("browser_click", 30)); w.Code != http.StatusOK {
		t.Fatal(w.Body)
	}
	srv := httptest.NewUnstartedServer(c.s.httpServer.Handler)
	srv.Config.WriteTimeout = 100 * time.Millisecond
	srv.Start()
	defer srv.Close()

	type result struct {
		status int
		body   string
		err    error
	}
	got := make(chan result, 1)
	go func() {
		body := `{"tool":"browser_click","args":{"ref":"e1"}}`
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/agent/alfred/client-tools/call", strings.NewReader(body))
		req.Host = testHost
		req.Header.Set("Authorization", "Bearer "+testAgentToken)
		resp, err := srv.Client().Do(req)
		if err != nil {
			got <- result{err: err}
			return
		}
		defer resp.Body.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		got <- result{status: resp.StatusCode, body: buf.String()}
	}()

	req := c.firstPending(t)
	time.Sleep(300 * time.Millisecond) // longer than WriteTimeout
	if _, err := c.relay.Claim(req.ID, clienttools.ClientID(id)); err != nil {
		t.Fatal(err)
	}
	if err := c.relay.Settle(req.ID, clienttools.ClientID(id), clienttools.Result{OK: true, Content: json.RawMessage(`[{"type":"text","text":"late"}]`)}); err != nil {
		t.Fatal(err)
	}
	r := <-got
	if r.err != nil || r.status != http.StatusOK || !strings.Contains(r.body, "late") {
		t.Fatalf("call after write timeout: status=%d body=%q err=%v", r.status, r.body, r.err)
	}
}

func TestClientToolsRoutesUnavailableWithoutRelay(t *testing.T) {
	s := newTokenSplitServer(t)
	w := chatRequest(t, s, http.MethodGet, "/api/v1/client-tools", testAPIToken, "")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", w.Code)
	}
}

// updateWireFixtures rewrites the generated wire fixtures from live relay
// state: LEO_UPDATE_FIXTURES=1 go test ./internal/web -run Fixture.
const updateWireFixtures = "LEO_UPDATE_FIXTURES"

// TestPendingResponseFixtureMatchesRelayState drives a relay into the three
// states a pending request can be in (claimed, unclaimed under an owner,
// unowned) and compares the route's response with pending-response.json, so
// the fixture cannot drift from Calls.Pending().
func TestPendingResponseFixtureMatchesRelayState(t *testing.T) {
	const (
		clientA = "ct_01JC8Z3K4M5N6P7Q8R9S0T1V2W"
		clientB = "ct_01JC8Z3K4M5N6P7Q8R9S0T1V3X"
	)
	clientIDs := []clienttools.ClientID{clientA, clientB}
	requestIDs := []string{"ctr_01JC8Z4A5B6C7D8E9F0G1H2J3K", "ctr_01JC8Z4A5B6C7D8E9F0G1H2J4M", "ctr_01JC8Z4A5B6C7D8E9F0G1H2J5N"}
	created := []time.Time{
		time.Date(2026, 10, 26, 15, 21, 2, 123_000_000, time.UTC),
		time.Date(2026, 10, 26, 15, 21, 3, 500_000_000, time.UTC),
		time.Date(2026, 10, 26, 15, 21, 4, 0, time.UTC),
	}
	var minted int
	c := newClientToolsServer(t)
	c.s.clientTools = clienttools.NewRelay(clienttools.RelayOptions{
		Registry: clienttools.RegistryOptions{
			IsBuiltin:   leotools.IsKnownTool,
			NewClientID: func() clienttools.ClientID { id := clientIDs[0]; clientIDs = clientIDs[1:]; return id },
		},
		Calls: clienttools.CallsOptions{
			Now:          func() time.Time { return created[minted-1] },
			NewRequestID: func() string { minted++; return requestIDs[minted-1] },
		},
		Flag: clienttools.NewEverRegistered(clienttools.NewFileFlagStore(filepath.Join(t.TempDir(), "client-tools.json")), nil),
	})
	c.relay = c.s.clientTools
	if a, b := string(c.relay.Connect()), string(c.relay.Connect()); a != clientA || b != clientB {
		t.Fatalf("ids = %s, %s", a, b)
	}
	for _, id := range []string{clientA, clientB} {
		if w := c.register(t, id, "browser", ctTool("browser_click", 30), ctTool("browser_snapshot", 30)); w.Code != http.StatusOK {
			t.Fatalf("register: %d %s", w.Code, w.Body)
		}
	}
	park := func(agent, tool, args string) {
		t.Helper()
		before := len(c.relay.Pending())
		body := `{"tool":"` + tool + `","args":` + args + `}`
		go chatRequest(t, c.s, http.MethodPost, "/api/agent/"+agent+"/client-tools/call", testAgentToken, body)
		deadline := time.After(5 * time.Second)
		for len(c.relay.Pending()) == before {
			select {
			case <-deadline:
				t.Fatal("call was not parked")
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	t.Cleanup(func() { c.relay.CancelAgent("alfred"); c.relay.CancelAgent("beta") })

	park("alfred", "browser_click", `{"ref":"e12"}`)
	if w := c.do(t, http.MethodPost, "/api/v1/client-tools/requests/"+requestIDs[0]+"/claim", testAPIToken, map[string]string{"client_id": clientA}); w.Code != http.StatusOK {
		t.Fatalf("claim: %d %s", w.Code, w.Body)
	}
	park("alfred", "browser_snapshot", `{}`)
	park("beta", "browser_snapshot", `{}`)

	w := c.do(t, http.MethodGet, "/api/v1/client-tools/requests?pending=1", testAPIToken, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("pending: %d %s", w.Code, w.Body)
	}
	if os.Getenv(updateWireFixtures) != "" {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, w.Body.Bytes(), "", "  "); err != nil {
			t.Fatal(err)
		}
		pretty.WriteByte('\n')
		if err := os.WriteFile(filepath.Join("..", "clienttools", "testdata", "wire", "pending-response.json"), pretty.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	jsonEqualToFixture(t, "pending-response.json", w.Body.Bytes())
}
