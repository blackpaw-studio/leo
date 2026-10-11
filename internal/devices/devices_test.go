package devices

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a settable clock for expiry tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestStore(t *testing.T) (*Store, *fakeClock, string) {
	t.Helper()
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
	return NewStore(dir, clock.Now), clock, dir
}

func mustCode(t *testing.T, s *Store) string {
	t.Helper()
	code, _, err := s.NewPairingCode()
	if err != nil {
		t.Fatalf("NewPairingCode: %v", err)
	}
	return code
}

func TestPairIssuesTokenThatAuthenticates(t *testing.T) {
	s, _, _ := newTestStore(t)
	code := mustCode(t, s)

	dev, token, err := s.Pair(code, "Evan's iPhone")
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	if dev.ID == "" || dev.Name != "Evan's iPhone" || token == "" {
		t.Fatalf("Pair returned %+v token=%q", dev, token)
	}
	got, ok := s.Authenticate(token)
	if !ok || got.ID != dev.ID {
		t.Fatalf("Authenticate(token) = %+v, %v; want device %s", got, ok, dev.ID)
	}
	if _, ok := s.Authenticate(token + "x"); ok {
		t.Fatal("Authenticate accepted a wrong token")
	}
	if _, ok := s.Authenticate(""); ok {
		t.Fatal("Authenticate accepted an empty token")
	}
}

func TestPairingCodeIsSingleUse(t *testing.T) {
	s, _, _ := newTestStore(t)
	code := mustCode(t, s)
	if _, _, err := s.Pair(code, "a"); err != nil {
		t.Fatalf("first Pair: %v", err)
	}
	if _, _, err := s.Pair(code, "b"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("second Pair err = %v, want ErrInvalidCode", err)
	}
}

func TestPairingCodeExpiresAfterFiveMinutes(t *testing.T) {
	s, clock, _ := newTestStore(t)
	fresh := mustCode(t, s)
	stale := mustCode(t, s)

	clock.Advance(PairingCodeTTL - time.Second)
	if _, _, err := s.Pair(fresh, "a"); err != nil {
		t.Fatalf("Pair just before expiry: %v", err)
	}
	clock.Advance(time.Second)
	if _, _, err := s.Pair(stale, "b"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("Pair at expiry err = %v, want ErrInvalidCode", err)
	}
}

func TestWrongCodeDoesNotConsumeOutstandingCode(t *testing.T) {
	s, _, _ := newTestStore(t)
	code := mustCode(t, s)
	if _, _, err := s.Pair("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "x"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("wrong code err = %v, want ErrInvalidCode", err)
	}
	if _, _, err := s.Pair(code, "a"); err != nil {
		t.Fatalf("real code after a wrong guess: %v", err)
	}
}

func TestPairingCodeHasAtLeast128BitsOfEntropy(t *testing.T) {
	s, _, _ := newTestStore(t)
	code := mustCode(t, s)
	// Base32: 5 bits per character.
	if len(code)*5 < 128 {
		t.Fatalf("code %q carries %d bits, want >= 128", code, len(code)*5)
	}
}

func TestSecretsAreStoredHashedWithOwnerOnlyPerms(t *testing.T) {
	s, _, dir := newTestStore(t)
	code := mustCode(t, s)
	pending := readFile(t, filepath.Join(dir, dirName, pairingFile))
	if strings.Contains(pending, code) {
		t.Fatal("pairing code stored in plaintext")
	}
	_, token, err := s.Pair(code, "a")
	if err != nil {
		t.Fatal(err)
	}
	raw := readFile(t, filepath.Join(dir, dirName, devicesFile))
	if strings.Contains(raw, token) {
		t.Fatal("device token stored in plaintext")
	}
	sum := sha256.Sum256([]byte(token))
	if !strings.Contains(raw, hex.EncodeToString(sum[:])) {
		t.Fatal("device token hash not found in store")
	}
	for _, name := range []string{devicesFile, pairingFile} {
		info, err := os.Stat(filepath.Join(dir, dirName, name))
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("%s perm = %o, want 600", name, perm)
		}
	}
}

func TestRevokeIsSeenByAnotherStoreInstance(t *testing.T) {
	s, clock, dir := newTestStore(t)
	_, token, err := s.Pair(mustCode(t, s), "a")
	if err != nil {
		t.Fatal(err)
	}
	dev, ok := s.Authenticate(token) // warm the daemon's cache
	if !ok {
		t.Fatal("token did not authenticate")
	}

	cli := NewStore(dir, clock.Now) // e.g. `leo devices revoke` in another process
	if err := cli.Revoke(dev.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, ok := s.Authenticate(token); ok {
		t.Fatal("revoked token still authenticates on the cached store")
	}
	if err := cli.Revoke(dev.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Revoke err = %v, want ErrNotFound", err)
	}
}

func TestListReturnsPairedDevicesWithoutSecrets(t *testing.T) {
	s, _, _ := newTestStore(t)
	for _, name := range []string{"phone", "tablet"} {
		if _, _, err := s.Pair(mustCode(t, s), name); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "phone" || list[1].Name != "tablet" {
		t.Fatalf("List = %+v", list)
	}
}

func TestSetPushReplacesRegistration(t *testing.T) {
	s, _, _ := newTestStore(t)
	dev, _, err := s.Pair(mustCode(t, s), "a")
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	if err := s.SetPush(dev.ID, PushRegistration{Grant: "g1", Key: key, RelayURL: testRelayURL}); err != nil {
		t.Fatalf("SetPush: %v", err)
	}
	if err := s.SetPush(dev.ID, PushRegistration{Grant: "g2", Key: key, RelayURL: testRelayURL}); err != nil {
		t.Fatalf("SetPush replace: %v", err)
	}
	list, _ := s.List()
	if list[0].Push == nil || list[0].Push.Grant != "g2" {
		t.Fatalf("push = %+v, want grant g2", list[0].Push)
	}
	if err := s.SetPush("nope", PushRegistration{Grant: "g", Key: key, RelayURL: testRelayURL}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetPush unknown err = %v, want ErrNotFound", err)
	}
}

func TestValidatePush(t *testing.T) {
	good := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cases := []struct {
		name string
		reg  PushRegistration
		ok   bool
	}{
		{"valid", PushRegistration{Grant: "grant", Key: good, RelayURL: testRelayURL}, true},
		{"valid with path", PushRegistration{Grant: "grant", Key: good, RelayURL: "https://relay.example/v1"}, true},
		{"empty grant", PushRegistration{Key: good, RelayURL: testRelayURL}, false},
		{"huge grant", PushRegistration{Grant: strings.Repeat("g", maxGrantLen+1), Key: good, RelayURL: testRelayURL}, false},
		{"short key", PushRegistration{Grant: "g", Key: base64.StdEncoding.EncodeToString(make([]byte, 16)), RelayURL: testRelayURL}, false},
		{"long key", PushRegistration{Grant: "g", Key: base64.StdEncoding.EncodeToString(make([]byte, 33)), RelayURL: testRelayURL}, false},
		{"not base64", PushRegistration{Grant: "g", Key: "!!!", RelayURL: testRelayURL}, false},
		{"missing relay url", PushRegistration{Grant: "g", Key: good}, false},
		{"http relay url", PushRegistration{Grant: "g", Key: good, RelayURL: "http://relay.example"}, false},
		{"relay url without host", PushRegistration{Grant: "g", Key: good, RelayURL: "https://"}, false},
		{"relay url with credentials", PushRegistration{Grant: "g", Key: good, RelayURL: "https://u:p@relay.example"}, false},
		{"relay url not a url", PushRegistration{Grant: "g", Key: good, RelayURL: "relay.example"}, false},
		{"relay url too long", PushRegistration{Grant: "g", Key: good, RelayURL: "https://relay.example/" + strings.Repeat("a", maxRelayURLLen)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePush(tc.reg); (err == nil) != tc.ok {
				t.Fatalf("ValidatePush err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestPairRejectsBadDeviceNames(t *testing.T) {
	s, _, _ := newTestStore(t)
	for _, name := range []string{"", "   ", "bad\nname", strings.Repeat("x", maxNameRunes+1)} {
		code := mustCode(t, s)
		if _, _, err := s.Pair(code, name); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("Pair(%q) err = %v, want ErrInvalidName", name, err)
		}
		// A rejected name must not burn the code.
		if _, _, err := s.Pair(code, "ok"); err != nil {
			t.Fatalf("code burned by a bad name: %v", err)
		}
	}
}

func TestEnsureDaemonIDIsStable(t *testing.T) {
	dir := t.TempDir()
	a, err := EnsureDaemonID(dir)
	if err != nil || a == "" {
		t.Fatalf("EnsureDaemonID = %q, %v", a, err)
	}
	b, err := EnsureDaemonID(dir)
	if err != nil || b != a {
		t.Fatalf("second EnsureDaemonID = %q, %v; want %q", b, err, a)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Codes are persisted (hashed) rather than held in memory, so a daemon
// restart neither forgets an outstanding code nor revives a consumed one.
func TestPairingCodeSurvivesRestartButNotReplay(t *testing.T) {
	cli, clock, dir := newTestStore(t)
	used := mustCode(t, cli)
	pending := mustCode(t, cli)
	if _, _, err := NewStore(dir, clock.Now).Pair(used, "a"); err != nil {
		t.Fatal(err)
	}

	restarted := NewStore(dir, clock.Now)
	if _, _, err := restarted.Pair(used, "replay"); !errors.Is(err, ErrInvalidCode) {
		t.Fatalf("replay after restart err = %v, want ErrInvalidCode", err)
	}
	if _, _, err := restarted.Pair(pending, "b"); err != nil {
		t.Fatalf("outstanding code after restart: %v", err)
	}
}

const testRelayURL = "https://relay.example"

func pairedWithPush(t *testing.T, s *Store, grant string) Device {
	t.Helper()
	dev, _, err := s.Pair(mustCode(t, s), "phone")
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	if err := s.SetPush(dev.ID, PushRegistration{Grant: grant, Key: key, RelayURL: testRelayURL}); err != nil {
		t.Fatal(err)
	}
	return dev
}

func TestPushRegistrationIsStoredWithItsDeviceOwnerOnly(t *testing.T) {
	s, _, dir := newTestStore(t)
	pairedWithPush(t, s, "g1")

	list, _ := s.List()
	if list[0].Push == nil || list[0].Push.RelayURL != testRelayURL {
		t.Fatalf("push = %+v, want relay %s", list[0].Push, testRelayURL)
	}
	info, err := os.Stat(filepath.Join(dir, dirName, devicesFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("devices.json perm = %o, want 600 (it holds push keys)", perm)
	}
}

func TestClearPushRemovesOnlyThatDevicesRegistration(t *testing.T) {
	s, _, _ := newTestStore(t)
	a := pairedWithPush(t, s, "ga")
	pairedWithPush(t, s, "gb")

	if err := s.ClearPush(a.ID); err != nil {
		t.Fatalf("ClearPush: %v", err)
	}
	if err := s.ClearPush(a.ID); err != nil {
		t.Fatalf("ClearPush is idempotent, got %v", err)
	}
	list, _ := s.List()
	if list[0].Push != nil || list[1].Push == nil {
		t.Fatalf("after clearing a: a.push=%v b.push=%v", list[0].Push, list[1].Push)
	}
	if err := s.ClearPush("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ClearPush unknown err = %v, want ErrNotFound", err)
	}
}

func TestClearPushIfGrantKeepsAFresherRegistration(t *testing.T) {
	s, _, _ := newTestStore(t)
	dev := pairedWithPush(t, s, "old")
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	if err := s.SetPush(dev.ID, PushRegistration{Grant: "new", Key: key, RelayURL: testRelayURL}); err != nil {
		t.Fatal(err)
	}

	if err := s.ClearPushIfGrant(dev.ID, "old"); err != nil {
		t.Fatal(err)
	}
	list, _ := s.List()
	if list[0].Push == nil || list[0].Push.Grant != "new" {
		t.Fatalf("a 410 for the old grant removed the new registration: %+v", list[0].Push)
	}
	if err := s.ClearPushIfGrant(dev.ID, "new"); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.List(); list[0].Push != nil {
		t.Fatalf("matching grant not removed: %+v", list[0].Push)
	}
	if err := s.ClearPushIfGrant("gone", "x"); err != nil {
		t.Fatalf("unknown device is a no-op, got %v", err)
	}
}

func TestRevokeRemovesThePushRegistration(t *testing.T) {
	s, _, dir := newTestStore(t)
	dev := pairedWithPush(t, s, "secret-grant")

	if err := s.Revoke(dev.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Fatalf("devices after revoke = %+v", list)
	}
	if raw := readFile(t, filepath.Join(dir, dirName, devicesFile)); strings.Contains(raw, "secret-grant") {
		t.Fatal("revoked device's push grant is still on disk")
	}
}
