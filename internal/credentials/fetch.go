package credentials

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Limits and rules mirror the Mac app's LeoPluginFetcher, so a request means
// the same thing whichever machine runs it.
const (
	// DefaultMaxBodyBytes caps a request body and a response body.
	DefaultMaxBodyBytes = 5 * 1024 * 1024
	maxHeaderCount      = 50
	maxHeaderValueBytes = 8 * 1024
	defaultFetchTimeout = 30 * time.Second
)

var (
	allowedMethods  = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD"}
	bodylessMethods = []string{"GET", "HEAD"}
	// Set by the transport, never by a caller.
	forbiddenHeaders = []string{
		"host", "cookie", "cookie2", "content-length", "transfer-encoding", "connection", "upgrade", "te", "expect",
		"keep-alive", "proxy-connection",
	}
	errOffHostRedirect = errors.New("redirect left the bound host")
)

// Request is a caller's HTTP request, run as an account. Headers are
// ordered pairs, as on the wire to the Mac app.
type Request struct {
	Kind       Kind        `json:"kind"`
	Dir        string      `json:"dir"`
	URL        string      `json:"url"`
	Method     string      `json:"method"`
	Headers    [][2]string `json:"headers"`
	BodyBase64 string      `json:"bodyBase64,omitempty"`
}

// Response is the upstream's answer. Headers repeat for a repeated name.
type Response struct {
	Status     int         `json:"status"`
	Headers    [][2]string `json:"headers"`
	BodyBase64 string      `json:"bodyBase64"`
}

// Resolver reads an account's token. TokenSource is the real one.
type Resolver interface {
	Resolve(ctx context.Context, kind Kind, dir string) (Credential, *Error)
}

// Fetcher performs requests as accounts. Source, Transport and Timeout are
// injected; the zero Timeout means 30 s and a nil Transport the default
// HTTP transport. There is no clock: nothing here depends on the time of
// day, and the deadline is a context timeout.
type Fetcher struct {
	Source    Resolver
	Transport http.RoundTripper
	Timeout   time.Duration
	// MaxBodyBytes caps request and response bodies; zero means
	// DefaultMaxBodyBytes (5 MiB).
	MaxBodyBytes int
}

func (f Fetcher) maxBody() int {
	if f.MaxBodyBytes > 0 {
		return f.MaxBodyBytes
	}
	return DefaultMaxBodyBytes
}

// Check resolves the account's token and nothing else. It is the validation
// a caller runs before saving an account, and sends no request.
func (f Fetcher) Check(ctx context.Context, accounts []Account, kind Kind, dir string) *Error {
	if _, ok := Find(accounts, kind, dir); !ok {
		return newError(ReasonInvalid, "this account is not available on this machine")
	}
	_, err := f.Source.Resolve(ctx, kind, dir)
	return err
}

// Fetch validates req, reads the account's token, makes the request and
// returns the response. Everything that can be refused is refused before the
// token is read, so a bad request never touches a secret. The token is sent
// only to the kind's bound host, over HTTPS, on the first hop and every
// redirect.
func (f Fetcher) Fetch(ctx context.Context, accounts []Account, req Request) (*Response, *Error) {
	if _, ok := Find(accounts, req.Kind, req.Dir); !ok {
		return nil, newError(ReasonInvalid, "this account is not available on this machine")
	}
	bound, _ := req.Kind.BoundHost()
	target, err := boundURL(req.URL, bound)
	if err != nil {
		return nil, err
	}
	method, headers, body, err := validate(req, f.maxBody())
	if err != nil {
		return nil, err
	}
	if err := checkReserved(req.Kind, headers); err != nil {
		return nil, err
	}

	cred, rerr := f.Source.Resolve(ctx, req.Kind, req.Dir)
	if rerr != nil {
		return nil, rerr
	}

	timeout := f.Timeout
	if timeout <= 0 {
		timeout = defaultFetchTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, herr := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if herr != nil {
		return nil, newError(ReasonInvalidRequest, "not a valid request")
	}
	for _, h := range headers {
		httpReq.Header.Add(h[0], h[1])
	}
	inject(httpReq.Header, req.Kind, cred)

	client := &http.Client{Transport: f.transport(), CheckRedirect: boundRedirects(bound)}
	resp, herr := client.Do(httpReq)
	if herr != nil {
		return nil, doError(herr)
	}
	defer resp.Body.Close()
	if resp.ContentLength > int64(f.maxBody()) {
		return nil, newError(ReasonBodyTooLarge, "the response is too large")
	}
	data, herr := io.ReadAll(io.LimitReader(resp.Body, int64(f.maxBody())+1))
	if herr != nil {
		return nil, newError(ReasonNetworkError, "the response could not be read")
	}
	if len(data) > f.maxBody() {
		return nil, newError(ReasonBodyTooLarge, "the response is too large")
	}
	return &Response{
		Status:     resp.StatusCode,
		Headers:    headerPairs(resp.Header),
		BodyBase64: base64.StdEncoding.EncodeToString(data),
	}, nil
}

func (f Fetcher) transport() http.RoundTripper {
	if f.Transport != nil {
		return f.Transport
	}
	return http.DefaultTransport
}

// boundURL parses raw and requires https on the bound host, with no userinfo
// and no port but 443. The returned URL has the host rewritten to the bound
// host, so the host that was checked is the host that is contacted.
func boundURL(raw, bound string) (*url.URL, *Error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, newError(ReasonInvalidRequest, "not a valid URL")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return nil, newError(ReasonInvalidRequest, "only https URLs are allowed")
	}
	if !onBoundHost(u, bound) {
		return nil, newError(ReasonHostMismatch, "this account can only be used with "+bound)
	}
	out := *u
	out.Scheme, out.Host, out.User, out.Fragment = "https", bound, nil, ""
	return &out, nil
}

func onBoundHost(u *url.URL, bound string) bool {
	port := u.Port()
	return u.User == nil && (port == "" || port == "443") &&
		strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), bound)
}

// boundRedirects re-checks every hop: https, the bound host, nothing else.
func boundRedirects(bound string) func(*http.Request, []*http.Request) error {
	return func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if next.URL.Scheme != "https" || !onBoundHost(next.URL, bound) {
			return errOffHostRedirect
		}
		return nil
	}
}

// doError maps a client failure to a wire error with a fixed message: the
// transport's own text can carry URLs and must not reach a response.
func doError(err error) *Error {
	switch {
	case errors.Is(err, errOffHostRedirect):
		return newError(ReasonHostMismatch, "a redirect left the account's host")
	case errors.Is(err, context.DeadlineExceeded):
		return newError(ReasonNetworkError, "the request timed out")
	}
	return newError(ReasonNetworkError, "the request failed")
}

// validate checks the caller's part of the request and returns it decoded.
func validate(req Request, maxBody int) (method string, headers [][2]string, body []byte, err *Error) {
	method = strings.ToUpper(req.Method)
	if method == "" {
		method = "GET"
	}
	if !slices.Contains(allowedMethods, method) {
		return "", nil, nil, newError(ReasonInvalidRequest, "method "+method+" is not allowed")
	}
	if len(req.Headers) > maxHeaderCount {
		return "", nil, nil, newError(ReasonInvalidRequest, "too many headers")
	}
	for _, h := range req.Headers {
		if err := validateHeader(h[0], h[1]); err != nil {
			return "", nil, nil, err
		}
	}
	if req.BodyBase64 != "" {
		if slices.Contains(bodylessMethods, method) {
			return "", nil, nil, newError(ReasonInvalidRequest, method+" requests can't have a body")
		}
		if len(req.BodyBase64) > base64.StdEncoding.EncodedLen(maxBody) {
			return "", nil, nil, newError(ReasonInvalidRequest, "request body is too large")
		}
		decoded, derr := base64.StdEncoding.DecodeString(req.BodyBase64)
		if derr != nil {
			return "", nil, nil, newError(ReasonInvalidRequest, "request body is not valid base64")
		}
		if len(decoded) > maxBody {
			return "", nil, nil, newError(ReasonInvalidRequest, "request body is too large")
		}
		body = decoded
	}
	return method, req.Headers, body, nil
}

func validateHeader(name, value string) *Error {
	lower := strings.ToLower(name)
	if name == "" || strings.IndexFunc(name, func(r rune) bool { return !isTokenChar(r) }) >= 0 {
		return newError(ReasonInvalidRequest, "header name '"+name+"' is not valid")
	}
	if slices.Contains(forbiddenHeaders, lower) || strings.HasPrefix(lower, "proxy-") {
		return newError(ReasonInvalidRequest, "header '"+name+"' can't be set")
	}
	if len(value) > maxHeaderValueBytes || strings.ContainsAny(value, "\r\n\x00") {
		return newError(ReasonInvalidRequest, "value of header '"+name+"' is not valid")
	}
	return nil
}

// isTokenChar reports an RFC 9110 token character.
func isTokenChar(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)
}

// reservedHeaders are the headers the daemon injects per kind (lowercased).
// A caller may not set them: a caller value must never replace an account's.
var reservedHeaders = map[Kind][]string{
	KindClaudeCode: {"authorization", "anthropic-beta", "user-agent"},
	KindCodex:      {"authorization", "chatgpt-account-id"},
}

func checkReserved(kind Kind, headers [][2]string) *Error {
	for _, h := range headers {
		if slices.Contains(reservedHeaders[kind], strings.ToLower(h[0])) {
			return newError(ReasonHeaderConflict, "header '"+h[0]+"' is set by the account")
		}
	}
	return nil
}

func inject(h http.Header, kind Kind, cred Credential) {
	h.Set("Authorization", "Bearer "+cred.Token)
	switch kind {
	case KindClaudeCode:
		h.Set("anthropic-beta", "oauth-2025-04-20")
		h.Set("User-Agent", "claude-cli/2.1.0 (external, cli)")
	case KindCodex:
		if cred.AccountID != "" {
			h.Set("ChatGPT-Account-Id", cred.AccountID)
		}
	}
}

func headerPairs(h http.Header) [][2]string {
	out := make([][2]string, 0, len(h))
	for _, name := range slices.Sorted(maps.Keys(h)) {
		for _, v := range h[name] {
			out = append(out, [2]string{name, v})
		}
	}
	return out
}
