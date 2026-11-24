package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/credentials"
)

const credTestHome = "/Users/tester"

type credResolver struct {
	cred  credentials.Credential
	err   *credentials.Error
	calls int
}

func (r *credResolver) Resolve(context.Context, credentials.Kind, string) (credentials.Credential, *credentials.Error) {
	r.calls++
	return r.cred, r.err
}

type credTransport struct {
	seen []*http.Request
	fn   func(*http.Request) *http.Response
}

func (c *credTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.seen = append(c.seen, r)
	return c.fn(r), nil
}

const credConfigYAML = `
environments:
  blackpaw:
    CLAUDE_CONFIG_DIR: ~/.claude-blackpaw
  codex-alt:
    CODEX_HOME: /srv/codex-alt
`

type credHarness struct {
	client *http.Client
	res    *credResolver
	tr     *credTransport
}

func startCredentialServer(t *testing.T) credHarness {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(testConfigYAML+credConfigYAML), 0600); err != nil {
		t.Fatal(err)
	}
	h := credHarness{
		res: &credResolver{cred: credentials.Credential{Token: "tok-FAKE-SECRET"}},
		tr: &credTransport{fn: func(*http.Request) *http.Response {
			return &http.Response{
				StatusCode: 200, ContentLength: -1,
				Header: http.Header{"X-Up": {"1"}},
				Body:   io.NopCloser(strings.NewReader("usage-body")),
			}
		}},
	}
	sockPath := tmpSockPath(t, "cr.sock")
	s := New(sockPath, cfgPath, nil, WithCredentials(credentials.Fetcher{
		Source: h.res, Transport: h.tr, Timeout: 5 * time.Second,
	}, credTestHome))
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	t.Cleanup(func() { s.Shutdown() }) //nolint:errcheck
	h.client = socketHTTPClient(sockPath)
	return h
}

func (h credHarness) do(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestCredentialsAccountsListsDefaultsAndEnvironments(t *testing.T) {
	h := startCredentialServer(t)
	status, body := h.do(t, "GET", "/credentials/accounts", "")
	if status != 200 {
		t.Fatalf("status %d: %s", status, body)
	}
	want := `{"accounts":[` +
		`{"kind":"claude-code","dir":"/Users/tester/.claude","label":".claude"},` +
		`{"kind":"codex","dir":"/Users/tester/.codex","label":".codex"},` +
		`{"kind":"claude-code","dir":"/Users/tester/.claude-blackpaw","label":"blackpaw"},` +
		`{"kind":"codex","dir":"/srv/codex-alt","label":"codex-alt"}]}`
	if strings.TrimSpace(body) != want {
		t.Errorf("body =\n%s\nwant\n%s", body, want)
	}
	if h.res.calls != 0 {
		t.Error("listing accounts must not read any token")
	}
}

func TestCredentialsAccountsFiltersByKind(t *testing.T) {
	h := startCredentialServer(t)
	_, body := h.do(t, "GET", "/credentials/accounts?kind=codex", "")
	var got struct {
		Accounts []credentials.Account `json:"accounts"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Accounts) != 2 || got.Accounts[0].Kind != "codex" || got.Accounts[1].Kind != "codex" {
		t.Errorf("accounts = %+v, want the two codex accounts", got.Accounts)
	}
	_, body = h.do(t, "GET", "/credentials/accounts?kind=nope", "")
	if strings.TrimSpace(body) != `{"accounts":[]}` {
		t.Errorf("unknown kind body = %s, want an empty list (not null)", body)
	}
}

func TestCredentialsCheck(t *testing.T) {
	h := startCredentialServer(t)
	status, body := h.do(t, "POST", "/credentials/check", `{"kind":"claude-code","dir":"/Users/tester/.claude-blackpaw"}`)
	if status != 200 || strings.TrimSpace(body) != `{"ok":true}` {
		t.Errorf("listed dir: %d %s", status, body)
	}
	status, body = h.do(t, "POST", "/credentials/check", `{"kind":"claude-code","dir":"/Users/tester/.ssh"}`)
	if status != 200 || !strings.Contains(body, `"reason":"credentialInvalid"`) {
		t.Errorf("unlisted dir: %d %s", status, body)
	}
	h.res.err = &credentials.Error{Reason: credentials.ReasonNeedsAuthorization, Message: "needs access"}
	status, body = h.do(t, "POST", "/credentials/check", `{"kind":"claude-code","dir":"/Users/tester/.claude"}`)
	if status != 200 || strings.TrimSpace(body) != `{"error":{"reason":"credentialNeedsAuthorization","message":"needs access"}}` {
		t.Errorf("needs authorization: %d %s", status, body)
	}
}

func TestCredentialsFetchRoundTrip(t *testing.T) {
	h := startCredentialServer(t)
	reqBody := `{"kind":"claude-code","dir":"/Users/tester/.claude-blackpaw","url":"https://api.anthropic.com/api/oauth/usage","method":"GET","headers":[["Accept","application/json"]]}`
	status, body := h.do(t, "POST", "/credentials/fetch", reqBody)
	if status != 200 {
		t.Fatalf("status %d: %s", status, body)
	}
	want := `{"status":200,"headers":[["X-Up","1"]],"bodyBase64":"` + base64.StdEncoding.EncodeToString([]byte("usage-body")) + `"}`
	if strings.TrimSpace(body) != want {
		t.Errorf("body = %s\nwant %s", body, want)
	}
	if strings.Contains(body, "SECRET") {
		t.Error("response leaks the token")
	}
	if got := h.tr.seen[0].Header.Get("Authorization"); got != "Bearer tok-FAKE-SECRET" {
		t.Errorf("upstream Authorization = %q", got)
	}
}

func TestCredentialsFetchRefusalsAre200WithAnErrorBody(t *testing.T) {
	h := startCredentialServer(t)
	for name, tc := range map[string]struct{ body, reason string }{
		"host mismatch":   {`{"kind":"claude-code","dir":"/Users/tester/.claude","url":"https://evil.example/x"}`, "credentialHostMismatch"},
		"header conflict": {`{"kind":"claude-code","dir":"/Users/tester/.claude","url":"https://api.anthropic.com/x","headers":[["authorization","x"]]}`, "credentialHeaderConflict"},
		"unlisted dir":    {`{"kind":"claude-code","dir":"/etc","url":"https://api.anthropic.com/x"}`, "credentialInvalid"},
		"bad method":      {`{"kind":"claude-code","dir":"/Users/tester/.claude","url":"https://api.anthropic.com/x","method":"TRACE"}`, "invalidRequest"},
	} {
		t.Run(name, func(t *testing.T) {
			status, body := h.do(t, "POST", "/credentials/fetch", tc.body)
			if status != 200 || !strings.Contains(body, `"reason":"`+tc.reason+`"`) || strings.Contains(body, `"status"`) {
				t.Errorf("%d %s, want 200 with reason %s", status, body, tc.reason)
			}
		})
	}
	if len(h.tr.seen) != 0 || h.res.calls != 0 {
		t.Errorf("refused requests reached the token (%d) or the network (%d)", h.res.calls, len(h.tr.seen))
	}
}

func TestCredentialsRoutesRejectMalformedJSONWith400(t *testing.T) {
	h := startCredentialServer(t)
	for _, path := range []string{"/credentials/check", "/credentials/fetch"} {
		if status, body := h.do(t, "POST", path, `{not json`); status != http.StatusBadRequest {
			t.Errorf("%s = %d %s, want 400", path, status, body)
		}
	}
}
