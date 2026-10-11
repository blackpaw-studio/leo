package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/devices"
)

// testClock is a settable clock shared by the pairing and heartbeat tests.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock {
	return &testClock{t: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// newDeviceTestServer is an unwrapped server (no auto-auth) with a device
// store on a fake clock, for exercising the pairing and device-token paths
// through the real middleware chain.
func newDeviceTestServer(t *testing.T, extra ...func(*Options)) (*Server, *devices.Store, *testClock) {
	t.Helper()
	dir := t.TempDir()
	clock := newTestClock()
	store := devices.NewStore(dir, clock.Now)
	opts := Options{Port: testPort, APIToken: testAPIToken, AgentToken: "agent-token", Devices: store}
	for _, fn := range extra {
		fn(&opts)
	}
	s := New(writeTestConfig(t, dir), &mockProcesses{states: map[string]ProcessStateInfo{
		"assistant": {Name: "assistant", Status: "running"},
	}}, &mockScheduler{}, &mockReloader{}, nil, opts)
	s.now = clock.Now
	return s, store, clock
}

func doRequest(s *Server, method, path, bearer, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = testHost
	req.RemoteAddr = "192.0.2.50:4000"
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

type pairResult struct {
	DeviceID string `json:"device_id"`
	Token    string `json:"token"`
}

func pairDevice(t *testing.T, s *Server, store *devices.Store, name string) pairResult {
	t.Helper()
	code, _, err := store.NewPairingCode()
	if err != nil {
		t.Fatal(err)
	}
	rec := doRequest(s, http.MethodPost, "/api/v1/pair", "", `{"pairing_code":"`+code+`","device_name":"`+name+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("pair status = %d body=%s", rec.Code, rec.Body)
	}
	var resp struct {
		OK   bool       `json:"ok"`
		Data pairResult `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Data.DeviceID == "" || resp.Data.Token == "" {
		t.Fatalf("pair response = %s", rec.Body)
	}
	return resp.Data
}

func TestPairRouteIsUnauthenticatedAndIssuesWorkingToken(t *testing.T) {
	s, store, _ := newDeviceTestServer(t)
	dev := pairDevice(t, s, store, "phone")

	if rec := doRequest(s, http.MethodGet, "/api/v1/state", dev.Token, ""); rec.Code != http.StatusOK {
		t.Fatalf("device token on /api/v1/state: status %d body=%s", rec.Code, rec.Body)
	}
	// Device tokens are operator-equivalent: they pass operator-only routes
	// that the agent token cannot.
	if rec := doRequest(s, http.MethodPost, "/api/v1/agents/nope/interrupt", dev.Token, ""); rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
		t.Fatalf("device token refused on operator route: %d %s", rec.Code, rec.Body)
	}
	if rec := doRequest(s, http.MethodGet, "/tasks", dev.Token, ""); rec.Code != http.StatusOK {
		t.Fatalf("device token on browser route: status %d", rec.Code)
	}
}

func TestPairRouteRejectsReusedAndUnknownCodes(t *testing.T) {
	s, store, _ := newDeviceTestServer(t)
	code, _, _ := store.NewPairingCode()
	body := `{"pairing_code":"` + code + `","device_name":"phone"}`
	if rec := doRequest(s, http.MethodPost, "/api/v1/pair", "", body); rec.Code != http.StatusOK {
		t.Fatalf("first pair: %d %s", rec.Code, rec.Body)
	}
	if rec := doRequest(s, http.MethodPost, "/api/v1/pair", "", body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("reused code: status %d, want 401", rec.Code)
	}
	if rec := doRequest(s, http.MethodPost, "/api/v1/pair", "", `{"pairing_code":"nope","device_name":"x"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown code: status %d, want 401", rec.Code)
	}
	if rec := doRequest(s, http.MethodPost, "/api/v1/pair", "", `{"pairing_code":`); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: status %d, want 400", rec.Code)
	}
}

func TestPairRouteIsRateLimited(t *testing.T) {
	s, store, clock := newDeviceTestServer(t)
	bad := `{"pairing_code":"WRONGWRONGWRONGWRONGWRONGWRONGWR","device_name":"x"}`
	for i := 0; i < pairPerPeerLimit; i++ {
		if rec := doRequest(s, http.MethodPost, "/api/v1/pair", "", bad); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d, want 401", i+1, rec.Code)
		}
	}
	// Once limited, even the right code is refused: the limiter runs before
	// the code is looked at, so guesses cost the same as hits.
	code, _, _ := store.NewPairingCode()
	good := `{"pairing_code":"` + code + `","device_name":"phone"}`
	rec := doRequest(s, http.MethodPost, "/api/v1/pair", "", good)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("over limit: status %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("Retry-After = %q, want 60", got)
	}
	clock.Advance(pairWindow)
	if rec := doRequest(s, http.MethodPost, "/api/v1/pair", "", good); rec.Code != http.StatusOK {
		t.Fatalf("after window: status %d body=%s", rec.Code, rec.Body)
	}
}

func TestPairRouteGlobalLimitSpansPeers(t *testing.T) {
	s, _, _ := newDeviceTestServer(t)
	bad := `{"pairing_code":"WRONG","device_name":"x"}`
	send := func(peer string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/pair", strings.NewReader(bad))
		req.Host = testHost
		req.RemoteAddr = peer + ":1"
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	for i := 0; i < pairGlobalLimit; i++ {
		if code := send("198.51.100." + strconv.Itoa(i)); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d", i+1, code)
		}
	}
	if code := send("203.0.113.1"); code != http.StatusTooManyRequests {
		t.Fatalf("fresh peer past global limit: status %d, want 429", code)
	}
}

func TestPairRouteUnavailableWithoutDeviceStore(t *testing.T) {
	s, _ := newRawTestServer(t)
	if rec := doRequest(s, http.MethodPost, "/api/v1/pair", "", `{"pairing_code":"x","device_name":"y"}`); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
}

func TestRevokedDeviceTokenIsRejected(t *testing.T) {
	s, store, _ := newDeviceTestServer(t)
	dev := pairDevice(t, s, store, "phone")
	if err := store.Revoke(dev.DeviceID); err != nil {
		t.Fatal(err)
	}
	if rec := doRequest(s, http.MethodGet, "/api/v1/state", dev.Token, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token: status %d, want 401", rec.Code)
	}
}

const testRelayURL = "https://relay.example"

func pushBody(key, relay string) string {
	return `{"grant":"g1","key":"` + key + `","relay_url":"` + relay + `"}`
}

func storedPush(t *testing.T, store *devices.Store, id string) *devices.PushRegistration {
	t.Helper()
	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range list {
		if d.ID == id {
			return d.Push
		}
	}
	t.Fatalf("device %s not found", id)
	return nil
}

func TestDevicePushRouteAuthMatrix(t *testing.T) {
	validKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cases := []struct {
		name   string
		bearer func(phone, tablet pairResult) string
		// target is which device's path is addressed.
		target     func(phone, tablet pairResult) string
		wantStatus int
	}{
		{"device writes its own", func(p, _ pairResult) string { return p.Token }, func(p, _ pairResult) string { return p.DeviceID }, http.StatusOK},
		{"device writes another device's", func(_, t pairResult) string { return t.Token }, func(p, _ pairResult) string { return p.DeviceID }, http.StatusForbidden},
		{"operator api.token writes any", func(_, _ pairResult) string { return testAPIToken }, func(p, _ pairResult) string { return p.DeviceID }, http.StatusOK},
		{"agent token refused", func(_, _ pairResult) string { return "agent-token" }, func(p, _ pairResult) string { return p.DeviceID }, http.StatusForbidden},
		{"no credentials", func(_, _ pairResult) string { return "" }, func(p, _ pairResult) string { return p.DeviceID }, http.StatusUnauthorized},
		{"garbage token", func(_, _ pairResult) string { return "nope" }, func(p, _ pairResult) string { return p.DeviceID }, http.StatusUnauthorized},
		{"operator, unknown device", func(_, _ pairResult) string { return testAPIToken }, func(_, _ pairResult) string { return "deadbeef" }, http.StatusNotFound},
		{"device, unknown id", func(p, _ pairResult) string { return p.Token }, func(_, _ pairResult) string { return "deadbeef" }, http.StatusForbidden},
	}
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		for _, tc := range cases {
			t.Run(method+"/"+tc.name, func(t *testing.T) {
				s, store, _ := newDeviceTestServer(t)
				phone := pairDevice(t, s, store, "phone")
				tablet := pairDevice(t, s, store, "tablet")
				body := ""
				if method == http.MethodPut {
					body = pushBody(validKey, testRelayURL)
				}
				path := "/api/v1/devices/" + tc.target(phone, tablet) + "/push"

				rec := doRequest(s, method, path, tc.bearer(phone, tablet), body)

				if rec.Code != tc.wantStatus {
					t.Fatalf("status %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body)
				}
				if tc.wantStatus != http.StatusOK && storedPush(t, store, phone.DeviceID) != nil {
					t.Fatal("a refused request changed the registration")
				}
			})
		}
	}
}

func TestPutStoresRegistrationAndDeleteRemovesIt(t *testing.T) {
	s, store, _ := newDeviceTestServer(t)
	phone := pairDevice(t, s, store, "phone")
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	path := "/api/v1/devices/" + phone.DeviceID + "/push"

	if rec := doRequest(s, http.MethodPut, path, phone.Token, pushBody(key, testRelayURL)); rec.Code != http.StatusOK {
		t.Fatalf("PUT status %d body=%s", rec.Code, rec.Body)
	}
	got := storedPush(t, store, phone.DeviceID)
	if got == nil || got.Grant != "g1" || got.Key != key || got.RelayURL != testRelayURL {
		t.Fatalf("stored push = %+v", got)
	}

	if rec := doRequest(s, http.MethodDelete, path, phone.Token, ""); rec.Code != http.StatusOK {
		t.Fatalf("DELETE status %d body=%s", rec.Code, rec.Body)
	}
	if got := storedPush(t, store, phone.DeviceID); got != nil {
		t.Fatalf("registration survived DELETE: %+v", got)
	}
	// Unregistering twice is harmless.
	if rec := doRequest(s, http.MethodDelete, path, phone.Token, ""); rec.Code != http.StatusOK {
		t.Fatalf("second DELETE status %d, want 200", rec.Code)
	}
}

func TestPutValidationErrors(t *testing.T) {
	s, store, _ := newDeviceTestServer(t)
	phone := pairDevice(t, s, store, "phone")
	good := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cases := map[string]struct {
		body string
		want int
	}{
		"http relay":        {pushBody(good, "http://relay.example"), http.StatusBadRequest},
		"missing relay":     {`{"grant":"g","key":"` + good + `"}`, http.StatusBadRequest},
		"relay with creds":  {pushBody(good, "https://u:p@relay.example"), http.StatusBadRequest},
		"short key":         {pushBody(base64.StdEncoding.EncodeToString(make([]byte, 16)), testRelayURL), http.StatusBadRequest},
		"key not base64":    {pushBody("!!!", testRelayURL), http.StatusBadRequest},
		"empty grant":       {`{"grant":"","key":"` + good + `","relay_url":"` + testRelayURL + `"}`, http.StatusBadRequest},
		"not json":          {`nope`, http.StatusBadRequest},
		"oversized payload": {`{"grant":"` + strings.Repeat("g", maxPushBody) + `"}`, http.StatusRequestEntityTooLarge},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := doRequest(s, http.MethodPut, "/api/v1/devices/"+phone.DeviceID+"/push", phone.Token, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d (body=%s)", rec.Code, tc.want, rec.Body)
			}
			if storedPush(t, store, phone.DeviceID) != nil {
				t.Fatal("an invalid registration was stored")
			}
		})
	}
}

func TestRevokedDevicesRegistrationIsGone(t *testing.T) {
	s, store, _ := newDeviceTestServer(t)
	phone := pairDevice(t, s, store, "phone")
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	path := "/api/v1/devices/" + phone.DeviceID + "/push"
	if rec := doRequest(s, http.MethodPut, path, phone.Token, pushBody(key, testRelayURL)); rec.Code != http.StatusOK {
		t.Fatalf("PUT status %d", rec.Code)
	}

	if err := store.Revoke(phone.DeviceID); err != nil {
		t.Fatal(err)
	}

	if list, _ := store.List(); len(list) != 0 {
		t.Fatalf("revoked device still listed with its registration: %+v", list)
	}
	if rec := doRequest(s, http.MethodPut, path, phone.Token, pushBody(key, testRelayURL)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token re-registering: status %d, want 401", rec.Code)
	}
}

func TestPublicURLHostIsAcceptedWithoutListenerPort(t *testing.T) {
	s, _, _ := newDeviceTestServer(t, func(o *Options) {
		o.PublicURLs = []string{"https://leo.example.com", "http://lan.example:9000"}
	})
	cases := []struct {
		host, origin string
		want         int
	}{
		{"leo.example.com", "", http.StatusOK},
		{"leo.example.com", "https://leo.example.com", http.StatusOK},
		{"lan.example:9000", "", http.StatusOK},
		{"leo.example.com:9999", "", http.StatusForbidden},
		{"lan.example", "", http.StatusForbidden},
		{"leo.example.com", "https://evil.example", http.StatusForbidden},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/state", nil)
		req.Host = tc.host
		req.RemoteAddr = "192.0.2.50:1"
		req.Header.Set("Authorization", "Bearer "+testAPIToken)
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("host=%q origin=%q: status %d, want %d", tc.host, tc.origin, rec.Code, tc.want)
		}
	}
}

// ssoRequest builds a request as a ForwardAuth proxy would forward it: from
// peer, with Remote-User set, and an optional bearer.
func ssoRequest(method, path, peer, bearer, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = testHost
	req.RemoteAddr = peer + ":4000"
	req.Header.Set("Remote-User", "evan")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return req
}

func serve(s *Server, req *http.Request) int {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Code
}

// SSO (PR #169) and device tokens are separate credentials: an SSO identity
// is operator-equivalent on the routes it already reached, but never stands
// in for a device on the device-only route, and gains nothing at /pair.
func TestSSOAndDeviceTokensDoNotCrossOver(t *testing.T) {
	const proxy, outsider = "192.0.2.9", "192.0.2.50"
	s, store, _ := newDeviceTestServer(t, func(o *Options) { o.TrustedProxies = []string{proxy} })
	phone := pairDevice(t, s, store, "phone")
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	push := pushBody(key, testRelayURL)
	pushPath := "/api/v1/devices/" + phone.DeviceID + "/push"

	if got := serve(s, ssoRequest(http.MethodPut, pushPath, proxy, "", push)); got != http.StatusForbidden {
		t.Errorf("SSO identity writing a device's push registration: %d, want 403", got)
	}
	if got := serve(s, ssoRequest(http.MethodPost, "/api/v1/pair", proxy, "", `{"pairing_code":"WRONG","device_name":"x"}`)); got != http.StatusUnauthorized {
		t.Errorf("SSO identity at /pair with a bad code: %d, want 401", got)
	}
	// A forged Remote-User from an untrusted peer authenticates nothing.
	if got := serve(s, ssoRequest(http.MethodGet, "/api/v1/state", outsider, "", "")); got != http.StatusUnauthorized {
		t.Errorf("forged Remote-User from untrusted peer: %d, want 401", got)
	}
	if got := serve(s, ssoRequest(http.MethodPut, pushPath, outsider, "", push)); got != http.StatusUnauthorized {
		t.Errorf("forged Remote-User on push: %d, want 401", got)
	}
	// Behind the proxy, a device still authenticates with its own token and
	// may write only its own registration.
	if got := serve(s, ssoRequest(http.MethodPut, pushPath, proxy, phone.Token, push)); got != http.StatusOK {
		t.Errorf("device token through the proxy: %d, want 200", got)
	}
	// A device token is not an SSO identity: it does not unlock the
	// Remote-User-only path (no header -> still bearer-checked).
	req := httptest.NewRequest(http.MethodGet, "/api/v1/state", nil)
	req.Host, req.RemoteAddr = testHost, proxy+":1"
	req.Header.Set("Authorization", "Bearer not-a-device")
	if got := serve(s, req); got != http.StatusUnauthorized {
		t.Errorf("bad bearer from trusted proxy without Remote-User: %d, want 401", got)
	}
}

// public_urls admits exactly the configured host[:port] and scheme://origin;
// it adds nothing to allowed_hosts, including for trusted proxy peers.
func TestPublicURLsDoNotWidenAllowedHosts(t *testing.T) {
	const proxy = "192.0.2.9"
	s, _, _ := newDeviceTestServer(t, func(o *Options) {
		o.PublicURLs = []string{"https://leo.example.com"}
		o.TrustedProxies = []string{proxy}
	})
	cases := []struct {
		name, peer, host, origin string
		want                     int
	}{
		{"exact public host", "192.0.2.50", "leo.example.com", "", http.StatusOK},
		{"public host on the listener port", "192.0.2.50", "leo.example.com:8370", "", http.StatusForbidden},
		{"public host on an explicit 443", "192.0.2.50", "leo.example.com:443", "", http.StatusForbidden},
		{"public origin, wrong scheme", "192.0.2.50", "leo.example.com", "http://leo.example.com", http.StatusForbidden},
		{"subdomain of public host", "192.0.2.50", "evil.leo.example.com", "", http.StatusForbidden},
		{"trusted proxy, public host with port", proxy, "leo.example.com:9443", "", http.StatusForbidden},
		{"public host, cross-origin page", "192.0.2.50", "leo.example.com", "https://evil.example", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/state", nil)
			req.Host, req.RemoteAddr = tc.host, tc.peer+":1"
			req.Header.Set("Authorization", "Bearer "+testAPIToken)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if got := serve(s, req); got != tc.want {
				t.Fatalf("status %d, want %d", got, tc.want)
			}
		})
	}
}

// Once the global budget is spent, new peers must not be added to the table:
// rotating source addresses (IPv6) would otherwise grow it without bound.
func TestPairLimiterTableStaysBounded(t *testing.T) {
	l := newPairLimiter()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for i := 0; i < pairGlobalLimit; i++ {
		if ok, _ := l.allow("198.51.100."+strconv.Itoa(i), now); !ok {
			t.Fatalf("attempt %d refused", i)
		}
	}
	for i := 0; i < 5000; i++ {
		if ok, _ := l.allow("2001:db8::"+strconv.FormatInt(int64(i), 16), now); ok {
			t.Fatal("allowed past the global limit")
		}
	}
	if n := len(l.peers); n > pairGlobalLimit {
		t.Fatalf("table holds %d peers after the global limit, want <= %d", n, pairGlobalLimit)
	}
	// Across many windows the table never exceeds its cap.
	for w := 0; w < 200; w++ {
		now = now.Add(pairWindow)
		for i := 0; i < pairGlobalLimit+50; i++ {
			l.allow("2001:db8:1::"+strconv.Itoa(w)+":"+strconv.Itoa(i), now)
		}
		if n := len(l.peers); n > pairPeerTableMax {
			t.Fatalf("window %d: table holds %d peers, cap %d", w, n, pairPeerTableMax)
		}
	}
}
