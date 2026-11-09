package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/devices"
	"github.com/blackpaw-studio/leo/internal/secretenv"
)

const (
	remoteTokenValue = "peer-token-4c1d"
	remoteHostCfg    = `
client:
  hosts:
    laptop:
      api_url: http://10.0.4.7:8370
      api_token: ` + remoteTokenValue + `
      api_client: dionysus
    defaulted:
      api_url: https://leo.example.com
      api_token: other-token
    vaulted:
      api_url: http://10.0.4.9:8370
      api_token:
        secret: keychain
        service: leo-peer
    sshonly:
      ssh: me@box
`
)

type stubSecretResolver struct {
	value string
	err   error
	calls []secretenv.Ref
}

func (s *stubSecretResolver) Resolve(r secretenv.Ref) (string, error) {
	s.calls = append(s.calls, r)
	return s.value, s.err
}

func newRemoteHostServer(t *testing.T, res *stubSecretResolver, extra ...func(*Options)) *Server {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(testConfigWithTemplatesYAML+remoteHostCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o750); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Port:       testPort,
		APIToken:   testAPIToken,
		AgentToken: testAgentToken,
		Clients:    []ClientPolicy{{Name: "docker-scout", Token: testClientToken, CanMessage: []string{"scout-*"}}},
	}
	for _, fn := range extra {
		fn(&opts)
	}
	s := New(cfgPath, &mockProcesses{states: map[string]ProcessStateInfo{}}, nil, nil, &mockAgentService{}, opts)
	s.secretResolver = res
	s.hostname = func() (string, error) { return "Dionysus.local", nil }
	return s
}

type remoteHostBody struct {
	APIURL    string `json:"api_url"`
	APIToken  string `json:"api_token"`
	APIClient string `json:"api_client"`
}

func decodeRemoteHost(t *testing.T, raw []byte) remoteHostBody {
	t.Helper()
	var env struct {
		Data remoteHostBody `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return env.Data
}

func TestRemoteHostRoute(t *testing.T) {
	vault := &stubSecretResolver{value: "from-the-vault"}
	s := newRemoteHostServer(t, vault)

	t.Run("agent token gets url, token and client name", func(t *testing.T) {
		w := requestAs(t, s, "GET", "/api/remote-host/laptop", testAgentToken, "")
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		want := remoteHostBody{APIURL: "http://10.0.4.7:8370", APIToken: remoteTokenValue, APIClient: "dionysus"}
		if got := decodeRemoteHost(t, w.Body.Bytes()); got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})
	t.Run("operator token works too", func(t *testing.T) {
		if w := requestAs(t, s, "GET", "/api/remote-host/laptop", testAPIToken, ""); w.Code != http.StatusOK {
			t.Fatalf("status %d", w.Code)
		}
	})
	t.Run("api_client defaults to the lowercased short hostname", func(t *testing.T) {
		w := requestAs(t, s, "GET", "/api/remote-host/defaulted", testAgentToken, "")
		if got := decodeRemoteHost(t, w.Body.Bytes()); got.APIClient != "dionysus" || got.APIURL != "https://leo.example.com" {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("secret reference is resolved on the daemon", func(t *testing.T) {
		w := requestAs(t, s, "GET", "/api/remote-host/vaulted", testAgentToken, "")
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
		if got := decodeRemoteHost(t, w.Body.Bytes()); got.APIToken != "from-the-vault" {
			t.Fatalf("token = %q", got.APIToken)
		}
		if len(vault.calls) != 1 || vault.calls[0].Service != "leo-peer" {
			t.Fatalf("resolver calls = %+v", vault.calls)
		}
	})
	t.Run("unknown host is a 404 naming it", func(t *testing.T) {
		w := requestAs(t, s, "GET", "/api/remote-host/nope", testAgentToken, "")
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `unknown host \"nope\"`) {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
	})
	t.Run("host without api_url is a 404 naming it", func(t *testing.T) {
		w := requestAs(t, s, "GET", "/api/remote-host/sshonly", testAgentToken, "")
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `host \"sshonly\" has no api_url`) {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
	})
	t.Run("a client token is refused", func(t *testing.T) {
		w := requestAs(t, s, "GET", "/api/remote-host/laptop", testClientToken, "")
		if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), remoteTokenValue) {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
	})
	t.Run("no token is refused", func(t *testing.T) {
		if w := requestAs(t, s, "GET", "/api/remote-host/laptop", "", ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("status %d", w.Code)
		}
	})
}

func TestRemoteHostRoute_SecretFailureNamesRefNotValue(t *testing.T) {
	vault := &stubSecretResolver{value: "must-not-leak", err: &secretenv.ResolutionError{Reason: "item not found"}}
	s := newRemoteHostServer(t, vault)
	w := requestAs(t, s, "GET", "/api/remote-host/vaulted", testAgentToken, "")
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if !strings.Contains(body, "client.hosts.vaulted.api_token") || strings.Contains(body, "must-not-leak") {
		t.Fatalf("body = %s", body)
	}
}

func TestRemoteHostRoute_HostnameUnavailable(t *testing.T) {
	s := newRemoteHostServer(t, &stubSecretResolver{})
	s.hostname = func() (string, error) { return "", errors.New("boom") }
	w := requestAs(t, s, "GET", "/api/remote-host/defaulted", testAgentToken, "")
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "api_client") {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	// An explicit api_client does not need the hostname at all.
	if w := requestAs(t, s, "GET", "/api/remote-host/laptop", testAgentToken, ""); w.Code != http.StatusOK {
		t.Fatalf("explicit api_client: status %d", w.Code)
	}
}

// A paired device's token is operator-equivalent on most routes, but this one
// returns a peer's credential: only the agent and operator API tokens may read it.
func TestRemoteHostRoute_PairedDeviceTokenRefused(t *testing.T) {
	store := devices.NewStore(t.TempDir(), time.Now)
	s := newRemoteHostServer(t, &stubSecretResolver{}, func(o *Options) { o.Devices = store })
	phone := pairDevice(t, s, store, "phone")

	w := doRequest(s, http.MethodGet, "/api/remote-host/laptop", phone.Token, "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403: %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), remoteTokenValue) {
		t.Fatalf("response leaks the peer token: %s", w.Body)
	}
	// The same server still serves the legitimate callers.
	for _, tok := range []string{testAgentToken, testAPIToken} {
		if w := doRequest(s, http.MethodGet, "/api/remote-host/laptop", tok, ""); w.Code != http.StatusOK {
			t.Errorf("token %q: status %d", tok, w.Code)
		}
	}
}
