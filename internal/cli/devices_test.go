package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/devices"
	"rsc.io/qr"
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

// TestRenderQRRoundTrips decodes the half-block rendering back into modules
// and compares it with the encoder's own bitmap, so a swapped top/bottom or
// an off-by-one quiet zone cannot ship an unscannable code.
func TestRenderQRRoundTrips(t *testing.T) {
	const text = `{"daemon_id":"abc","pairing_code":"XYZ"}`
	var out bytes.Buffer
	if err := renderQR(&out, text); err != nil {
		t.Fatal(err)
	}
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		t.Fatal(err)
	}
	const quiet = 2
	var grid [][]bool // grid[y][x], quiet zone included
	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		line = strings.TrimSuffix(strings.TrimPrefix(line, "\x1b[30;47m"), "\x1b[0m")
		var top, bottom []bool
		for _, r := range line {
			top = append(top, r == '█' || r == '▀')
			bottom = append(bottom, r == '█' || r == '▄')
		}
		grid = append(grid, top, bottom)
	}
	for y := 0; y < code.Size; y++ {
		for x := 0; x < code.Size; x++ {
			if grid[y+quiet][x+quiet] != code.Black(x, y) {
				t.Fatalf("module (%d,%d) = %v, want %v", x, y, grid[y+quiet][x+quiet], code.Black(x, y))
			}
		}
	}
	if len(grid[0]) != code.Size+2*quiet {
		t.Fatalf("row width %d, want %d", len(grid[0]), code.Size+2*quiet)
	}
}

func TestPairJSONPrintsPayloadFieldsWarningsExpiryAndQRPayload(t *testing.T) {
	cfg := pairTestConfig(t, config.WebConfig{Enabled: true, PublicURLs: []string{"https://leo.example.com"}})
	var out bytes.Buffer

	err := runJSON(&out, func() (any, error) { return pairJSON(cfg, "studio-mac", noIfaces) })

	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		devices.Payload
		Warnings  []string  `json:"warnings"`
		ExpiresAt time.Time `json:"expires_at"`
		QRPayload string    `json:"qr_payload"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not one JSON object: %q", out.String())
	}
	if got.Name != "studio-mac" || got.PairingCode == "" || len(got.URLs) != 1 {
		t.Fatalf("payload = %+v", got.Payload)
	}
	if !strings.Contains(out.String(), `"warnings":[]`) {
		t.Fatalf("warnings must be an empty array, not null: %s", out.String())
	}
	if !got.ExpiresAt.After(time.Now()) {
		t.Fatalf("expires_at = %v", got.ExpiresAt)
	}
	var fromQR devices.Payload
	if err := json.Unmarshal([]byte(got.QRPayload), &fromQR); err != nil || !reflect.DeepEqual(fromQR, got.Payload) {
		t.Fatalf("qr_payload %q does not decode to the payload (%v)", got.QRPayload, err)
	}
	if _, _, err := devices.NewStore(cfg.StatePath(), nil).Pair(got.PairingCode, "phone"); err != nil {
		t.Fatalf("redeeming the JSON code: %v", err)
	}
}

func TestPairJSONCarriesReachabilityWarnings(t *testing.T) {
	cfg := pairTestConfig(t, config.WebConfig{Enabled: true})
	var out bytes.Buffer

	_ = runJSON(&out, func() (any, error) { return pairJSON(cfg, "mac", noIfaces) })

	var got struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got.Warnings) == 0 || !strings.Contains(got.Warnings[0], "web.public_urls") {
		t.Fatalf("stdout = %s", out.String())
	}
}

func TestPairJSONWebDisabledIsTheExactCodedObjectAndNonZero(t *testing.T) {
	cfg := pairTestConfig(t, config.WebConfig{Enabled: false})
	var out bytes.Buffer

	err := runJSON(&out, func() (any, error) { return pairJSON(cfg, "mac", noIfaces) })

	if got, want := out.String(), "{\"error\":\"web_disabled\"}\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if ExitCode(err) != 1 {
		t.Fatalf("exit = %d", ExitCode(err))
	}
}
