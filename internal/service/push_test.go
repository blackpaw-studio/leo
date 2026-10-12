package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/devices"
	"github.com/blackpaw-studio/leo/internal/observe"
	"github.com/blackpaw-studio/leo/internal/push"
)

// An attention transition published through the daemon's wired publisher
// reaches a paired device's relay as a push it can decrypt, and a repeat of
// the same state does not.
func TestWirePushDeliversAttentionTransitionsToPairedDevices(t *testing.T) {
	stateDir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	store := devices.NewStore(stateDir, nil)
	code, _, err := store.NewPairingCode()
	if err != nil {
		t.Fatal(err)
	}
	dev, _, err := store.Pair(code, "phone")
	if err != nil {
		t.Fatal(err)
	}

	pushes := make(chan map[string]string, 4)
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		pushes <- body
		w.WriteHeader(http.StatusAccepted)
	}))
	defer relay.Close()
	if err := store.SetPush(dev.ID, devices.PushRegistration{
		Grant: "grant", Key: base64.StdEncoding.EncodeToString(key), RelayURL: relay.URL,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pub := wirePush(ctx, nil, stateDir, nil, push.WithHTTPClient(relay.Client()))
	attention := observe.NewAttentionStore(pub)

	attention.Set("builder", observe.AttentionFinished)
	attention.Set("builder", observe.AttentionFinished) // repeat: no push
	attention.Set("sentinel", observe.AttentionErrored)

	first := recvPush(t, pushes)
	second := recvPush(t, pushes)
	if first["grant"] != "grant" || first["collapse_id"] == second["collapse_id"] {
		t.Fatalf("pushes = %v, %v; want distinct collapse ids per agent", first, second)
	}
	daemonID, _ := devices.EnsureDaemonID(stateDir)
	if first["collapse_id"] != push.CollapseID(daemonID, "builder") || second["collapse_id"] != push.CollapseID(daemonID, "sentinel") {
		t.Fatalf("the repeat finished produced a push, or order broke: %v %v", first, second)
	}
	raw, _ := base64.StdEncoding.DecodeString(first["ciphertext"])
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, raw[:12], raw[12:], []byte("leo-push-v1"))
	if err != nil {
		t.Fatalf("device cannot decrypt the push: %v", err)
	}
	var got struct{ Agent, Kind string }
	_ = json.Unmarshal(plain, &got)
	if got.Agent != "builder" || got.Kind != "finished" {
		t.Fatalf("plaintext = %s", plain)
	}
}

func recvPush(t *testing.T, ch <-chan map[string]string) map[string]string {
	t.Helper()
	select {
	case p := <-ch:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("relay received no push")
		return nil
	}
}

// A policy handed to wirePush keeps a denied state off the relay while the
// allowed one right behind it still goes out.
func TestWirePushHonorsThePolicy(t *testing.T) {
	stateDir := t.TempDir()
	store := devices.NewStore(stateDir, nil)
	code, _, err := store.NewPairingCode()
	if err != nil {
		t.Fatal(err)
	}
	dev, _, err := store.Pair(code, "phone")
	if err != nil {
		t.Fatal(err)
	}
	pushes := make(chan map[string]string, 4)
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		pushes <- body
		w.WriteHeader(http.StatusAccepted)
	}))
	defer relay.Close()
	key := make([]byte, 32)
	if err := store.SetPush(dev.ID, devices.PushRegistration{
		Grant: "grant", Key: base64.StdEncoding.EncodeToString(key), RelayURL: relay.URL,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	allow := func(agent, state string) bool { return state != "finished" }
	pub := wirePush(ctx, nil, stateDir, allow, push.WithHTTPClient(relay.Client()))
	attention := observe.NewAttentionStore(pub)

	attention.Set("quiet", observe.AttentionFinished) // denied
	attention.Set("loud", observe.AttentionErrored)   // allowed

	daemonID, _ := devices.EnsureDaemonID(stateDir)
	got := recvPush(t, pushes)
	if got["collapse_id"] != push.CollapseID(daemonID, "loud") {
		t.Fatalf("first push = %v, want the allowed agent's (the denied one must not reach the relay)", got)
	}
}
