package credentials

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	claudeDir = "/Users/tester/.claude"
	codexDir  = "/Users/tester/.codex"
)

var testAccounts = []Account{
	{Kind: KindClaudeCode, Dir: claudeDir, Label: ".claude"},
	{Kind: KindCodex, Dir: codexDir, Label: ".codex"},
}

type fakeResolver struct {
	cred  Credential
	err   *Error
	calls int
}

func (f *fakeResolver) Resolve(context.Context, Kind, string) (Credential, *Error) {
	f.calls++
	return f.cred, f.err
}

// fakeTransport answers each request with the next handler's response and
// records what it was sent.
type fakeTransport struct {
	handler func(n int, r *http.Request) (*http.Response, error)
	seen    []*http.Request
}

func (f *fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.seen = append(f.seen, r)
	return f.handler(len(f.seen)-1, r)
}

func reply(status int, body string, headers ...string) *http.Response {
	h := http.Header{}
	for i := 0; i+1 < len(headers); i += 2 {
		h.Add(headers[i], headers[i+1])
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)), ContentLength: -1}
}

func okTransport() *fakeTransport {
	return &fakeTransport{handler: func(int, *http.Request) (*http.Response, error) { return reply(200, "{}"), nil }}
}

// testBodyCap keeps the body-cap tests small; the real cap is checked once.
const testBodyCap = 1024

func newFetcher(res *fakeResolver, tr *fakeTransport) Fetcher {
	return Fetcher{Source: res, Transport: tr, Timeout: 5 * time.Second, MaxBodyBytes: testBodyCap}
}

func claudeReq(url string, headers ...[2]string) Request {
	return Request{Kind: KindClaudeCode, Dir: claudeDir, URL: url, Headers: headers}
}

func mustFail(t *testing.T, err *Error, reason string) {
	t.Helper()
	if err == nil || err.Reason != reason {
		t.Fatalf("err = %v, want reason %s", err, reason)
	}
}

func TestFetchInjectsClaudeCredentialAndReturnsResponse(t *testing.T) {
	res := &fakeResolver{cred: Credential{Token: "tok-FAKE"}}
	tr := &fakeTransport{handler: func(int, *http.Request) (*http.Response, error) {
		return reply(201, "hello", "X-Thing", "a", "X-Thing", "b"), nil
	}}
	got, err := newFetcher(res, tr).Fetch(context.Background(), testAccounts,
		claudeReq("https://api.anthropic.com/api/oauth/usage?x=1", [2]string{"Accept", "application/json"}))
	if err != nil {
		t.Fatalf("Fetch() error: %v", err)
	}
	h := tr.seen[0].Header
	if h.Get("Authorization") != "Bearer tok-FAKE" || h.Get("anthropic-beta") != "oauth-2025-04-20" ||
		h.Get("User-Agent") != "claude-cli/2.1.0 (external, cli)" || h.Get("Accept") != "application/json" {
		t.Errorf("sent headers = %v", h)
	}
	if tr.seen[0].Method != "GET" || tr.seen[0].URL.RawQuery != "x=1" {
		t.Errorf("sent %s %s", tr.seen[0].Method, tr.seen[0].URL)
	}
	body, _ := base64.StdEncoding.DecodeString(got.BodyBase64)
	if got.Status != 201 || string(body) != "hello" {
		t.Errorf("response = %d %q", got.Status, body)
	}
	var xthing []string
	for _, p := range got.Headers {
		if strings.EqualFold(p[0], "x-thing") {
			xthing = append(xthing, p[1])
		}
	}
	if len(xthing) != 2 {
		t.Errorf("response headers = %v, want both X-Thing values", got.Headers)
	}
}

func TestFetchInjectsCodexAccountID(t *testing.T) {
	res := &fakeResolver{cred: Credential{Token: "tok-FAKE", AccountID: "acct-7"}}
	tr := okTransport()
	_, err := newFetcher(res, tr).Fetch(context.Background(), testAccounts,
		Request{Kind: KindCodex, Dir: codexDir, URL: "https://chatgpt.com/backend-api/wham/usage"})
	if err != nil {
		t.Fatalf("Fetch() error: %v", err)
	}
	h := tr.seen[0].Header
	if h.Get("Authorization") != "Bearer tok-FAKE" || h.Get("ChatGPT-Account-Id") != "acct-7" {
		t.Errorf("sent headers = %v", h)
	}
}

func TestFetchRejectsWrongHostBeforeReadingTheToken(t *testing.T) {
	for _, url := range []string{
		"https://evil.example/x",
		"https://api.anthropic.com.evil.example/x",
		"https://api.anthropic.com@evil.example/x",
		"https://evil.example@api.anthropic.com/x",
		"https://api.anthropic.com:8443/x",
		"https://chatgpt.com/x", // another kind's host
	} {
		res, tr := &fakeResolver{cred: Credential{Token: "t"}}, okTransport()
		_, err := newFetcher(res, tr).Fetch(context.Background(), testAccounts, claudeReq(url))
		mustFail(t, err, ReasonHostMismatch)
		if res.calls != 0 || len(tr.seen) != 0 {
			t.Errorf("%s: token read %d times, %d requests sent", url, res.calls, len(tr.seen))
		}
	}
}

func TestFetchRequiresHTTPS(t *testing.T) {
	res, tr := &fakeResolver{cred: Credential{Token: "t"}}, okTransport()
	_, err := newFetcher(res, tr).Fetch(context.Background(), testAccounts, claudeReq("http://api.anthropic.com/x"))
	mustFail(t, err, ReasonInvalidRequest)
	if len(tr.seen) != 0 {
		t.Error("a plain-http request was sent")
	}
}

func TestFetchStopsAtARedirectOffTheBoundHost(t *testing.T) {
	for name, location := range map[string]string{
		"other host":        "https://evil.example/steal",
		"downgrade to http": "http://api.anthropic.com/x",
	} {
		t.Run(name, func(t *testing.T) {
			tr := &fakeTransport{handler: func(n int, _ *http.Request) (*http.Response, error) {
				return reply(302, "", "Location", location), nil
			}}
			_, err := newFetcher(&fakeResolver{cred: Credential{Token: "t"}}, tr).Fetch(context.Background(), testAccounts, claudeReq("https://api.anthropic.com/x"))
			mustFail(t, err, ReasonHostMismatch)
			if len(tr.seen) != 1 {
				t.Errorf("%d requests sent, want the redirect target never contacted", len(tr.seen))
			}
		})
	}
}

func TestFetchFollowsASameHostRedirectKeepingTheCredential(t *testing.T) {
	tr := &fakeTransport{handler: func(n int, _ *http.Request) (*http.Response, error) {
		if n == 0 {
			return reply(301, "", "Location", "https://api.anthropic.com/new"), nil
		}
		return reply(200, "moved"), nil
	}}
	got, err := newFetcher(&fakeResolver{cred: Credential{Token: "tok-FAKE"}}, tr).Fetch(context.Background(), testAccounts, claudeReq("https://api.anthropic.com/old"))
	if err != nil || got.Status != 200 || len(tr.seen) != 2 {
		t.Fatalf("Fetch() = %+v, %v after %d requests", got, err, len(tr.seen))
	}
	if tr.seen[1].Header.Get("Authorization") != "Bearer tok-FAKE" {
		t.Errorf("redirected request lost its credential: %v", tr.seen[1].Header)
	}
}

func TestFetchRejectsCallerHeadersThatCollideWithInjectedOnes(t *testing.T) {
	cases := []struct {
		kind Kind
		dir  string
		url  string
		name string
	}{
		{KindClaudeCode, claudeDir, "https://api.anthropic.com/x", "authorization"},
		{KindClaudeCode, claudeDir, "https://api.anthropic.com/x", "Anthropic-Beta"},
		{KindClaudeCode, claudeDir, "https://api.anthropic.com/x", "USER-AGENT"},
		{KindCodex, codexDir, "https://chatgpt.com/x", "Authorization"},
		{KindCodex, codexDir, "https://chatgpt.com/x", "chatgpt-account-id"},
	}
	for _, c := range cases {
		res, tr := &fakeResolver{cred: Credential{Token: "t"}}, okTransport()
		_, err := newFetcher(res, tr).Fetch(context.Background(), testAccounts,
			Request{Kind: c.kind, Dir: c.dir, URL: c.url, Headers: [][2]string{{c.name, "x"}}})
		mustFail(t, err, ReasonHeaderConflict)
		if res.calls != 0 || len(tr.seen) != 0 {
			t.Errorf("%s/%s: token read or request sent despite the conflict", c.kind, c.name)
		}
	}
}

func TestFetchValidatesTheRequestLikeTheApp(t *testing.T) {
	many := make([][2]string, 51)
	for i := range many {
		many[i] = [2]string{"X-N" + string(rune('a'+i%26)) + string(rune('a'+i/26)), "v"}
	}
	big := bytes.Repeat([]byte("a"), testBodyCap+1)
	cases := map[string]Request{
		"bad method":           {Method: "TRACE"},
		"too many headers":     {Headers: many},
		"forbidden header":     {Headers: [][2]string{{"Cookie", "a=b"}}},
		"proxy header":         {Headers: [][2]string{{"Proxy-Authorization", "x"}}},
		"empty header name":    {Headers: [][2]string{{"", "v"}}},
		"bad header name":      {Headers: [][2]string{{"X Bad", "v"}}},
		"CRLF in value":        {Headers: [][2]string{{"X-A", "a\r\nX-B: 1"}}},
		"NUL in value":         {Headers: [][2]string{{"X-A", "a\x00"}}},
		"oversize value":       {Headers: [][2]string{{"X-A", strings.Repeat("a", maxHeaderValueBytes+1)}}},
		"body on GET":          {Method: "GET", BodyBase64: base64.StdEncoding.EncodeToString([]byte("x"))},
		"body on HEAD":         {Method: "HEAD", BodyBase64: base64.StdEncoding.EncodeToString([]byte("x"))},
		"invalid base64":       {Method: "POST", BodyBase64: "!!!"},
		"request body too big": {Method: "POST", BodyBase64: base64.StdEncoding.EncodeToString(big)},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			req.Kind, req.Dir = KindClaudeCode, claudeDir
			req.URL = "https://api.anthropic.com/x"
			res, tr := &fakeResolver{cred: Credential{Token: "t"}}, okTransport()
			_, err := newFetcher(res, tr).Fetch(context.Background(), testAccounts, req)
			mustFail(t, err, ReasonInvalidRequest)
			if res.calls != 0 || len(tr.seen) != 0 {
				t.Error("token read or request sent for an invalid request")
			}
		})
	}
}

func TestFetchSendsABodyOnPOST(t *testing.T) {
	var sent []byte
	tr := &fakeTransport{handler: func(_ int, r *http.Request) (*http.Response, error) {
		sent, _ = io.ReadAll(r.Body)
		return reply(200, ""), nil
	}}
	req := claudeReq("https://api.anthropic.com/x")
	req.Method, req.BodyBase64 = "post", base64.StdEncoding.EncodeToString([]byte(`{"a":1}`))
	if _, err := newFetcher(&fakeResolver{cred: Credential{Token: "t"}}, tr).Fetch(context.Background(), testAccounts, req); err != nil {
		t.Fatalf("Fetch() error: %v", err)
	}
	if string(sent) != `{"a":1}` || tr.seen[0].Method != "POST" {
		t.Errorf("sent %s %q", tr.seen[0].Method, sent)
	}
}

func TestFetchCapsTheResponseBody(t *testing.T) {
	for name, tc := range map[string]struct {
		size   int
		length int64
		reason string
	}{
		"streamed past the cap": {testBodyCap + 1, -1, ReasonBodyTooLarge},
		"declared past the cap": {10, testBodyCap + 1, ReasonBodyTooLarge},
		"exactly at the cap":    {testBodyCap, -1, ""},
	} {
		t.Run(name, func(t *testing.T) {
			tr := &fakeTransport{handler: func(int, *http.Request) (*http.Response, error) {
				r := reply(200, strings.Repeat("a", tc.size))
				r.ContentLength = tc.length
				return r, nil
			}}
			_, err := newFetcher(&fakeResolver{cred: Credential{Token: "t"}}, tr).Fetch(context.Background(), testAccounts, claudeReq("https://api.anthropic.com/x"))
			if tc.reason == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			mustFail(t, err, tc.reason)
		})
	}
}

func TestFetchSurfacesNeedsAuthorizationWithoutSending(t *testing.T) {
	res := &fakeResolver{err: newError(ReasonNeedsAuthorization, "the Keychain would need to ask for access")}
	tr := okTransport()
	_, err := newFetcher(res, tr).Fetch(context.Background(), testAccounts, claudeReq("https://api.anthropic.com/x"))
	mustFail(t, err, ReasonNeedsAuthorization)
	if len(tr.seen) != 0 {
		t.Error("a request was sent without a token")
	}
}

func TestFetchRejectsAnAccountThatIsNotListed(t *testing.T) {
	for _, dir := range []string{"/Users/tester/.ssh", "/Users/tester/.claude/../.ssh", "relative/dir", "", "/Users/tester/.codex"} {
		res, tr := &fakeResolver{cred: Credential{Token: "t"}}, okTransport()
		req := claudeReq("https://api.anthropic.com/x")
		req.Dir = dir
		_, err := newFetcher(res, tr).Fetch(context.Background(), testAccounts, req)
		mustFail(t, err, ReasonInvalid)
		if res.calls != 0 || len(tr.seen) != 0 {
			t.Errorf("dir %q: token read or request sent", dir)
		}
	}
	res := &fakeResolver{}
	req := claudeReq("https://api.anthropic.com/x")
	req.Kind = "other"
	_, err := newFetcher(res, okTransport()).Fetch(context.Background(), testAccounts, req)
	mustFail(t, err, ReasonInvalid)
}

func TestFetchErrorsNeverContainTheToken(t *testing.T) {
	const token = "tok-VERY-SECRET-123"
	res := &fakeResolver{cred: Credential{Token: token, AccountID: "acct-SECRET"}}
	failures := map[string]*fakeTransport{
		"transport error": {handler: func(int, *http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed carrying " + token)
		}},
		"bad redirect": {handler: func(int, *http.Request) (*http.Response, error) {
			return reply(302, "", "Location", "https://evil.example/?t="+token), nil
		}},
	}
	for name, tr := range failures {
		t.Run(name, func(t *testing.T) {
			_, err := newFetcher(res, tr).Fetch(context.Background(), testAccounts, claudeReq("https://api.anthropic.com/x"))
			if err == nil {
				t.Fatal("want an error")
			}
			wire, _ := json.Marshal(err)
			if strings.Contains(string(wire), "SECRET") || strings.Contains(err.Error(), "SECRET") {
				t.Errorf("error leaks token material: %s", wire)
			}
		})
	}
	if strings.Contains(Credential{Token: token}.String(), "SECRET") {
		t.Error("Credential.String leaks the token")
	}
}

func TestFetchTimesOutAsNetworkError(t *testing.T) {
	tr := &fakeTransport{handler: func(_ int, r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}}
	f := newFetcher(&fakeResolver{cred: Credential{Token: "t"}}, tr)
	f.Timeout = 20 * time.Millisecond
	_, err := f.Fetch(context.Background(), testAccounts, claudeReq("https://api.anthropic.com/x"))
	mustFail(t, err, ReasonNetworkError)
}

func TestCheckResolvesTheTokenAndNothingElse(t *testing.T) {
	res, tr := &fakeResolver{cred: Credential{Token: "t"}}, okTransport()
	f := newFetcher(res, tr)
	if err := f.Check(context.Background(), testAccounts, KindClaudeCode, claudeDir); err != nil {
		t.Fatalf("Check() error: %v", err)
	}
	if res.calls != 1 || len(tr.seen) != 0 {
		t.Errorf("token reads = %d, requests = %d; want 1 and 0", res.calls, len(tr.seen))
	}
	mustFail(t, f.Check(context.Background(), testAccounts, KindClaudeCode, "/Users/tester/.ssh"), ReasonInvalid)
	res.err = newError(ReasonNeedsAuthorization, "x")
	mustFail(t, f.Check(context.Background(), testAccounts, KindClaudeCode, claudeDir), ReasonNeedsAuthorization)
}

func TestFetchBodyCapDefaultsToFiveMiB(t *testing.T) {
	if got := (Fetcher{}).maxBody(); got != 5*1024*1024 {
		t.Errorf("default body cap = %d, want 5 MiB", got)
	}
}
