package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/devices"
)

func pairTestConfig(t *testing.T, web config.WebConfig) *config.Config {
	t.Helper()
	return &config.Config{HomePath: t.TempDir(), Web: web}
}

func noIfaces() ([]net.Addr, error) { return nil, nil }

// payloadFrom finds the one-line JSON payload in leo pair's output.
func payloadFrom(t *testing.T, out string) devices.Payload {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "{") {
			var p devices.Payload
			if err := json.Unmarshal([]byte(line), &p); err != nil {
				t.Fatalf("payload line %q: %v", line, err)
			}
			return p
		}
	}
	t.Fatalf("no JSON payload in output:\n%s", out)
	return devices.Payload{}
}

func TestPairPrintsRedeemablePayload(t *testing.T) {
	cfg := pairTestConfig(t, config.WebConfig{Enabled: true, PublicURLs: []string{"https://leo.example.com"}})
	var out, errOut bytes.Buffer
	if err := runPair(&out, &errOut, cfg, "studio-mac", noIfaces); err != nil {
		t.Fatalf("runPair: %v", err)
	}
	p := payloadFrom(t, out.String())
	if p.DaemonID == "" || p.Name != "studio-mac" || p.PairingCode == "" {
		t.Fatalf("payload = %+v", p)
	}
	if len(p.URLs) != 1 || p.URLs[0] != "https://leo.example.com" {
		t.Fatalf("urls = %q", p.URLs)
	}
	id, _ := devices.EnsureDaemonID(cfg.StatePath())
	if p.DaemonID != id {
		t.Fatalf("daemon_id %q != state daemon id %q", p.DaemonID, id)
	}
	// The printed code is the one the daemon will redeem.
	if _, _, err := devices.NewStore(cfg.StatePath(), nil).Pair(p.PairingCode, "phone"); err != nil {
		t.Fatalf("redeeming printed code: %v", err)
	}
	if !strings.Contains(out.String(), "▀") && !strings.Contains(out.String(), "▄") {
		t.Fatal("no QR code in output")
	}
}

func TestPairWarnsWhenPhoneCannotReachListener(t *testing.T) {
	cfg := pairTestConfig(t, config.WebConfig{Enabled: true})
	var out, errOut bytes.Buffer
	if err := runPair(&out, &errOut, cfg, "mac", noIfaces); err != nil {
		t.Fatalf("runPair: %v", err)
	}
	if !strings.Contains(errOut.String(), "web.public_urls") {
		t.Fatalf("no reachability warning for a loopback bind; stderr:\n%s", errOut.String())
	}
}

func TestPairWarnsWhenLANHostIsNotAllowed(t *testing.T) {
	cfg := pairTestConfig(t, config.WebConfig{Enabled: true, Bind: "192.168.1.20", AllowedHosts: []string{"leo.local"}})
	var out, errOut bytes.Buffer
	if err := runPair(&out, &errOut, cfg, "mac", noIfaces); err != nil {
		t.Fatalf("runPair: %v", err)
	}
	if !strings.Contains(errOut.String(), "web.allowed_hosts") {
		t.Fatalf("no allowed_hosts warning; stderr:\n%s", errOut.String())
	}
}

func TestPairRefusesWhenWebDisabled(t *testing.T) {
	cfg := pairTestConfig(t, config.WebConfig{Enabled: false})
	var out, errOut bytes.Buffer
	if err := runPair(&out, &errOut, cfg, "mac", noIfaces); err == nil {
		t.Fatal("want error when the web listener is disabled")
	}
}

func TestDevicesListAndRevoke(t *testing.T) {
	dir := t.TempDir()
	store := devices.NewStore(dir, func() time.Time { return time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC) })
	code, _, _ := store.NewPairingCode()
	dev, token, err := store.Pair(code, "Evan's iPhone")
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runDevicesList(&out, store); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), dev.ID) || !strings.Contains(out.String(), "Evan's iPhone") {
		t.Fatalf("list output:\n%s", out.String())
	}
	if strings.Contains(out.String(), dev.TokenHash) {
		t.Fatal("list printed the token hash")
	}

	out.Reset()
	if err := runDevicesRevoke(&out, store, dev.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Authenticate(token); ok {
		t.Fatal("token still valid after revoke")
	}
	if err := runDevicesRevoke(&out, store, dev.ID); !errors.Is(err, devices.ErrNotFound) {
		t.Fatalf("revoke unknown: %v", err)
	}
}
