package push

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 10, 11, 4, 0, 0, 0, time.UTC)

// decrypt is the contract's receiver side, written independently of the
// sender: base64(nonce(12) ‖ ciphertext ‖ tag(16)), AES-256-GCM, aad
// "leo-push-v1".
func decrypt(t *testing.T, key []byte, b64 string) (map[string]any, error) {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("ciphertext is not base64: %v", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < gcm.NonceSize()+gcm.Overhead() {
		t.Fatalf("ciphertext too short: %d bytes", len(raw))
	}
	plain, err := gcm.Open(nil, raw[:12], raw[12:], []byte("leo-push-v1"))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(plain, &out); err != nil {
		t.Fatalf("plaintext is not JSON: %v", err)
	}
	return out, nil
}

type pushBody struct {
	Grant      string `json:"grant"`
	Ciphertext string `json:"ciphertext"`
	CollapseID string `json:"collapse_id"`
}

// fakeRegistry is an in-memory Registry that records Forget calls.
type fakeRegistry struct {
	mu        sync.Mutex
	regs      []Registration
	forgotten []string
}

func (r *fakeRegistry) Registrations() ([]Registration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Registration(nil), r.regs...), nil
}

func (r *fakeRegistry) Forget(deviceID, grant string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.forgotten = append(r.forgotten, deviceID+"/"+grant)
	return nil
}

func (r *fakeRegistry) Forgotten() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.forgotten...)
}

// relay is an httptest TLS relay that records each /push body and answers
// with the next scripted status (the last one repeats).
type relay struct {
	*httptest.Server
	mu       sync.Mutex
	bodies   []pushBody
	headers  []http.Header
	statuses []int
}

func newRelay(t *testing.T, statuses ...int) *relay {
	t.Helper()
	if len(statuses) == 0 {
		statuses = []int{http.StatusAccepted}
	}
	r := &relay{statuses: statuses}
	r.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var b pushBody
		_ = json.NewDecoder(req.Body).Decode(&b)
		r.mu.Lock()
		idx := min(len(r.bodies), len(r.statuses)-1)
		status := r.statuses[idx]
		r.bodies = append(r.bodies, b)
		r.headers = append(r.headers, req.Header.Clone())
		if req.URL.Path != "/push" || req.Method != http.MethodPost {
			status = http.StatusNotFound
		}
		r.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *relay) Bodies() []pushBody {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]pushBody(nil), r.bodies...)
}

type harness struct {
	sender  *RelaySender
	reg     *fakeRegistry
	relay   *relay
	key     []byte
	logs    *bytes.Buffer
	sleeps  []time.Duration
	sleepMu sync.Mutex
}

// newHarness wires a RelaySender to a TLS relay with one registered device
// (id "dev1", grant "grant-1"), a fixed clock and an instant sleep.
func newHarness(t *testing.T, statuses ...int) *harness {
	t.Helper()
	h := &harness{relay: newRelay(t, statuses...), logs: &bytes.Buffer{}}
	h.key = bytes.Repeat([]byte{7}, 32)
	h.reg = &fakeRegistry{regs: []Registration{{DeviceID: "dev1", Grant: "grant-1", Key: h.key, RelayURL: h.relay.URL}}}
	h.sender = NewRelaySender(h.reg, "daemon-abc",
		WithHTTPClient(h.relay.Client()),
		WithClock(func() time.Time { return fixedNow }),
		WithSleep(func(_ context.Context, d time.Duration) error {
			h.sleepMu.Lock()
			h.sleeps = append(h.sleeps, d)
			h.sleepMu.Unlock()
			return nil
		}),
		WithLogger(log.New(h.logs, "", 0)),
	)
	return h
}

var alert = Alert{Agent: "builder", Kind: "needs_input", Title: "Needs permission: Bash", At: fixedNow}

func TestSendEncryptsPlaintextPerTheContract(t *testing.T) {
	h := newHarness(t)

	h.sender.Send(context.Background(), alert)

	bodies := h.relay.Bodies()
	if len(bodies) != 1 {
		t.Fatalf("relay got %d pushes, want 1", len(bodies))
	}
	if bodies[0].Grant != "grant-1" {
		t.Errorf("grant = %q, want grant-1", bodies[0].Grant)
	}
	got, err := decrypt(t, h.key, bodies[0].Ciphertext)
	if err != nil {
		t.Fatalf("receiver could not decrypt with the registered key: %v", err)
	}
	want := map[string]any{
		"v": float64(1), "daemon_id": "daemon-abc", "agent": "builder",
		"kind": "needs_input", "title": "Needs permission: Bash", "ts": float64(fixedNow.Unix()),
	}
	if len(got) != len(want) {
		t.Errorf("plaintext = %v, want exactly %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("plaintext[%s] = %v, want %v", k, got[k], v)
		}
	}
	if ct := h.relay.headers[0].Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestCiphertextIsBoundToKeyAndAAD(t *testing.T) {
	h := newHarness(t)
	h.sender.Send(context.Background(), alert)
	ct := h.relay.Bodies()[0].Ciphertext

	if _, err := decrypt(t, bytes.Repeat([]byte{8}, 32), ct); err == nil {
		t.Error("decrypted with the wrong key")
	}
	raw, _ := base64.StdEncoding.DecodeString(ct)
	block, _ := aes.NewCipher(h.key)
	gcm, _ := cipher.NewGCM(block)
	if _, err := gcm.Open(nil, raw[:12], raw[12:], []byte("leo-push-v2")); err == nil {
		t.Error("decrypted with the wrong aad")
	}
	if len(ct) > 2048 {
		t.Errorf("ciphertext is %d bytes, the relay caps it at 2048", len(ct))
	}
}

func TestEachSendUsesAFreshNonce(t *testing.T) {
	h := newHarness(t)
	h.sender.Send(context.Background(), alert)
	h.sender.Send(context.Background(), alert)

	bodies := h.relay.Bodies()
	a, _ := base64.StdEncoding.DecodeString(bodies[0].Ciphertext)
	b, _ := base64.StdEncoding.DecodeString(bodies[1].Ciphertext)
	if bytes.Equal(a[:12], b[:12]) {
		t.Fatal("two sends reused a GCM nonce")
	}
}

func TestOversizedTitleStillFitsTheRelayLimit(t *testing.T) {
	h := newHarness(t)
	huge := alert
	huge.Title = strings.Repeat("é", 5000)
	huge.Agent = strings.Repeat("a", 5000)

	h.sender.Send(context.Background(), huge)

	bodies := h.relay.Bodies()
	if len(bodies) != 1 || len(bodies[0].Ciphertext) > 2048 {
		t.Fatalf("bodies=%d, ciphertext len=%d; must send one push under 2048", len(bodies), len(bodies[0].Ciphertext))
	}
}

func TestCollapseIDIsHashOfDaemonAndAgent(t *testing.T) {
	sum := sha256.Sum256([]byte("daemon-abc/builder"))
	want := hex.EncodeToString(sum[:])[:32]
	if got := CollapseID("daemon-abc", "builder"); got != want {
		t.Fatalf("CollapseID = %q, want %q", got, want)
	}
	if CollapseID("daemon-abc", "other") == want || CollapseID("daemon-xyz", "builder") == want {
		t.Fatal("collapse id does not depend on both daemon and agent")
	}

	h := newHarness(t)
	h.sender.Send(context.Background(), alert)
	body := h.relay.Bodies()[0]
	if body.CollapseID != want {
		t.Errorf("wire collapse_id = %q, want %q", body.CollapseID, want)
	}
	if strings.Contains(body.CollapseID, "builder") {
		t.Error("agent name leaks through collapse_id")
	}
}

func TestSendsToEveryRegisteredDeviceWithItsOwnKeyAndGrant(t *testing.T) {
	h := newHarness(t)
	otherKey := bytes.Repeat([]byte{9}, 32)
	h.reg.regs = append(h.reg.regs, Registration{DeviceID: "dev2", Grant: "grant-2", Key: otherKey, RelayURL: h.relay.URL})

	h.sender.Send(context.Background(), alert)

	byGrant := map[string]pushBody{}
	for _, b := range h.relay.Bodies() {
		byGrant[b.Grant] = b
	}
	if len(byGrant) != 2 {
		t.Fatalf("pushes by grant = %v, want grant-1 and grant-2", byGrant)
	}
	if _, err := decrypt(t, h.key, byGrant["grant-1"].Ciphertext); err != nil {
		t.Errorf("dev1 payload not under dev1's key: %v", err)
	}
	if _, err := decrypt(t, otherKey, byGrant["grant-2"].Ciphertext); err != nil {
		t.Errorf("dev2 payload not under dev2's key: %v", err)
	}
}

func TestSendWithoutRegistrationsMakesNoRequests(t *testing.T) {
	h := newHarness(t)
	h.reg.regs = nil
	h.sender.Send(context.Background(), alert)
	if n := len(h.relay.Bodies()); n != 0 {
		t.Fatalf("relay got %d pushes with no registrations", n)
	}
}

func TestRetryPolicy(t *testing.T) {
	cases := []struct {
		name         string
		statuses     []int
		wantRequests int
		wantSleeps   int
	}{
		{"success is not retried", []int{202}, 1, 0},
		{"5xx then success retries once", []int{503, 202}, 2, 1},
		{"persistent 5xx stops after one retry", []int{500, 502, 503}, 2, 1},
		{"4xx is not retried", []int{400, 202}, 1, 0},
		{"401 bad grant is not retried", []int{401, 202}, 1, 0},
		{"429 is not retried", []int{429, 202}, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tc.statuses...)
			h.sender.Send(context.Background(), alert)
			if got := len(h.relay.Bodies()); got != tc.wantRequests {
				t.Errorf("requests = %d, want %d", got, tc.wantRequests)
			}
			if len(h.sleeps) != tc.wantSleeps {
				t.Errorf("backoff sleeps = %d, want %d", len(h.sleeps), tc.wantSleeps)
			}
			if len(h.reg.Forgotten()) != 0 {
				t.Errorf("registration forgotten on %v", tc.statuses)
			}
		})
	}
}

// flakyTransport fails the first n requests at the network layer.
type flakyTransport struct {
	mu    sync.Mutex
	fails int
	calls int
	next  http.RoundTripper
}

func (f *flakyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.calls++
	fail := f.calls <= f.fails
	f.mu.Unlock()
	if fail {
		return nil, errors.New("connection reset")
	}
	return f.next.RoundTrip(r)
}

func TestNetworkErrorIsRetriedOnce(t *testing.T) {
	for _, tc := range []struct {
		fails, wantCalls, wantPushes int
	}{{1, 2, 1}, {2, 2, 0}, {5, 2, 0}} {
		h := newHarness(t)
		ft := &flakyTransport{fails: tc.fails, next: h.relay.Client().Transport}
		h.sender = NewRelaySender(h.reg, "daemon-abc",
			WithHTTPClient(&http.Client{Transport: ft}),
			WithClock(func() time.Time { return fixedNow }),
			WithSleep(func(context.Context, time.Duration) error { return nil }),
			WithLogger(log.New(io.Discard, "", 0)))

		h.sender.Send(context.Background(), alert)

		if ft.calls != tc.wantCalls || len(h.relay.Bodies()) != tc.wantPushes {
			t.Errorf("fails=%d: calls=%d pushes=%d, want calls=%d pushes=%d",
				tc.fails, ft.calls, len(h.relay.Bodies()), tc.wantCalls, tc.wantPushes)
		}
	}
}

func TestEachAttemptHasATenSecondDeadline(t *testing.T) {
	var deadlines []time.Duration
	h := newHarness(t, 503, 202)
	h.sender = NewRelaySender(h.reg, "daemon-abc",
		WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			dl, ok := r.Context().Deadline()
			if !ok {
				t.Error("request has no deadline")
			}
			deadlines = append(deadlines, time.Until(dl))
			return h.relay.Client().Transport.RoundTrip(r)
		})}),
		WithClock(func() time.Time { return fixedNow }),
		WithSleep(func(context.Context, time.Duration) error { return nil }),
		WithLogger(log.New(io.Discard, "", 0)))

	h.sender.Send(context.Background(), alert)

	if len(deadlines) != 2 {
		t.Fatalf("attempts = %d, want 2", len(deadlines))
	}
	for i, d := range deadlines {
		if d <= 9*time.Second || d > 10*time.Second {
			t.Errorf("attempt %d deadline in %v, want ~10s each", i, d)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGone410ForgetsTheRegistrationWithoutRetry(t *testing.T) {
	h := newHarness(t, http.StatusGone, 202)

	h.sender.Send(context.Background(), alert)

	if got := h.reg.Forgotten(); len(got) != 1 || got[0] != "dev1/grant-1" {
		t.Fatalf("forgotten = %v, want [dev1/grant-1]", got)
	}
	if n := len(h.relay.Bodies()); n != 1 {
		t.Errorf("a 410 was retried: %d requests", n)
	}
}

func TestOneDevicesFailureDoesNotStopTheOthers(t *testing.T) {
	good := newRelay(t)
	bad := newRelay(t, http.StatusGone)
	h := newHarness(t)
	h.reg.regs = []Registration{
		{DeviceID: "gone", Grant: "g-gone", Key: h.key, RelayURL: bad.URL},
		{DeviceID: "ok", Grant: "g-ok", Key: h.key, RelayURL: good.URL},
	}
	// One client trusting both test servers' CAs is not possible, so route
	// each request to the right TLS client by host.
	h.sender = NewRelaySender(h.reg, "daemon-abc",
		WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if strings.Contains(bad.URL, r.URL.Host) {
				return bad.Client().Transport.RoundTrip(r)
			}
			return good.Client().Transport.RoundTrip(r)
		})}),
		WithClock(func() time.Time { return fixedNow }),
		WithSleep(func(context.Context, time.Duration) error { return nil }),
		WithLogger(log.New(io.Discard, "", 0)))

	h.sender.Send(context.Background(), alert)

	if len(good.Bodies()) != 1 {
		t.Errorf("healthy device got %d pushes, want 1", len(good.Bodies()))
	}
	if got := h.reg.Forgotten(); len(got) != 1 || got[0] != "gone/g-gone" {
		t.Errorf("forgotten = %v, want [gone/g-gone]", got)
	}
}

func TestRelayURLTrailingSlashAndPathAreHonoured(t *testing.T) {
	var gotPath string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)
	h := newHarness(t)
	h.reg.regs = []Registration{{DeviceID: "d", Grant: "g", Key: h.key, RelayURL: srv.URL + "/v1/"}}
	h.sender = NewRelaySender(h.reg, "daemon-abc", WithHTTPClient(srv.Client()),
		WithClock(func() time.Time { return fixedNow }), WithLogger(log.New(io.Discard, "", 0)))

	h.sender.Send(context.Background(), alert)

	if gotPath != "/v1/push" {
		t.Fatalf("posted to %q, want /v1/push", gotPath)
	}
}

func TestRegistrationWithoutRelayURLOrBadKeyIsSkipped(t *testing.T) {
	h := newHarness(t)
	h.reg.regs = []Registration{
		{DeviceID: "legacy", Grant: "g", Key: h.key},                                    // pre-relay_url registration
		{DeviceID: "shortkey", Grant: "g", Key: []byte("short"), RelayURL: h.relay.URL}, // malformed
		{DeviceID: "plain", Grant: "g", Key: h.key, RelayURL: strings.Replace(h.relay.URL, "https", "http", 1)},
	}
	h.sender.Send(context.Background(), alert)
	if n := len(h.relay.Bodies()); n != 0 {
		t.Fatalf("sent %d pushes for unusable registrations", n)
	}
}

func TestLogsNeverContainKeysGrantsOrPlaintext(t *testing.T) {
	secretTitle := "TOPSECRET-title"
	for _, statuses := range [][]int{{500}, {400}, {410}, {202}} {
		h := newHarness(t, statuses...)
		h.reg.regs[0].Grant = "SECRETGRANT"
		a := alert
		a.Title = secretTitle
		a.Agent = "TOPSECRET-agent"
		h.sender.Send(context.Background(), a)

		out := h.logs.String()
		keyB64 := base64.StdEncoding.EncodeToString(h.key)
		for _, leak := range []string{"SECRETGRANT", keyB64, hex.EncodeToString(h.key), secretTitle, "TOPSECRET-agent", "needs_input"} {
			if strings.Contains(out, leak) {
				t.Errorf("statuses %v: log leaks %q:\n%s", statuses, leak, out)
			}
		}
	}

	// Network-level failure too.
	h := newHarness(t)
	h.reg.regs[0].Grant = "SECRETGRANT"
	h.sender = NewRelaySender(h.reg, "daemon-abc",
		WithHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("boom")
		})}),
		WithSleep(func(context.Context, time.Duration) error { return nil }),
		WithLogger(log.New(h.logs, "", 0)))
	h.sender.Send(context.Background(), alert)
	if strings.Contains(h.logs.String(), "SECRETGRANT") {
		t.Errorf("log leaks the grant on network error: %s", h.logs)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var hits int
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	t.Cleanup(target.Close)
	redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/push", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	h := newHarness(t)
	h.reg.regs = []Registration{{DeviceID: "d", Grant: "g", Key: h.key, RelayURL: redirector.URL}}
	// The default client (no WithHTTPClient) must refuse redirects; swap
	// only its transport's trust via the option used by the hardening.
	h.sender = NewRelaySender(h.reg, "daemon-abc", WithTransport(redirector.Client().Transport),
		WithClock(func() time.Time { return fixedNow }), WithLogger(log.New(io.Discard, "", 0)))

	h.sender.Send(context.Background(), alert)

	if hits != 0 {
		t.Fatal("the sender followed a relay redirect")
	}
}
