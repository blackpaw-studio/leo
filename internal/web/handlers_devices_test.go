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

func TestDevicePushRegistrationIsOwnDeviceOnly(t *testing.T) {
	s, store, _ := newDeviceTestServer(t)
	phone := pairDevice(t, s, store, "phone")
	tablet := pairDevice(t, s, store, "tablet")
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	body := `{"grant":"g1","key":"` + key + `"}`
	path := "/api/v1/devices/" + phone.DeviceID + "/push"

	if rec := doRequest(s, http.MethodPut, path, phone.Token, body); rec.Code != http.StatusOK {
		t.Fatalf("own registration: status %d body=%s", rec.Code, rec.Body)
	}
	for name, tok := range map[string]string{"other device": tablet.Token, "operator": testAPIToken, "agent": "agent-token"} {
		if rec := doRequest(s, http.MethodPut, path, tok, `{"grant":"evil","key":"`+key+`"}`); rec.Code != http.StatusForbidden {
			t.Fatalf("%s writing phone's registration: status %d, want 403", name, rec.Code)
		}
	}
	if rec := doRequest(s, http.MethodPut, path, phone.Token, `{"grant":"g","key":"short"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad key: status %d, want 400", rec.Code)
	}
	list, _ := store.List()
	if list[0].Push == nil || list[0].Push.Grant != "g1" {
		t.Fatalf("stored push = %+v, want grant g1", list[0].Push)
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
