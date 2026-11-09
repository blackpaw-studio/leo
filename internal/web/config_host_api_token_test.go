package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/secretenv"
)

const hostTokenSentinel = "tok-sentinel-9f3a"

func hostWithAPIToken() config.HostConfig {
	tok := config.Literal(hostTokenSentinel)
	return config.HostConfig{
		SSH: "me@laptop", APIURL: "http://10.0.4.7:8370", APIToken: &tok, APIClient: "dionysus",
	}
}

func assertHostKeepsAPI(t *testing.T, got config.HostConfig) {
	t.Helper()
	if got.APIToken == nil {
		t.Fatal("api_token was dropped by a web save")
	}
	if lit, _ := got.APIToken.AsLiteral(); lit != hostTokenSentinel {
		t.Fatalf("api_token changed to %q", lit)
	}
	if got.APIURL != "http://10.0.4.7:8370" || got.APIClient != "dionysus" {
		t.Fatalf("api_url/api_client changed: %+v", got)
	}
}

// TestConfigHosts_EditPreservesAPIToken: the form and the JSON patch both
// replace the stored host, so a field the UI never renders must survive.
func TestConfigHosts_EditPreservesAPIToken(t *testing.T) {
	cfg := &config.Config{Client: config.ClientConfig{Hosts: map[string]config.HostConfig{"laptop": hostWithAPIToken()}}}
	s, dir := newTestServerWithConfigFile(t, cfg)
	configDir := strings.TrimSuffix(dir, "/leo.yaml")

	w := postForm(t, s, "/web/config/host/laptop", url.Values{
		"ssh":        {"me@elsewhere"},
		"api_url":    {"http://10.0.4.7:8370"},
		"api_client": {"dionysus"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("form save: %d %s", w.Code, readBody(t, w))
	}
	got := reloadTestConfig(t, configDir).Client.Hosts["laptop"]
	if got.SSH != "me@elsewhere" {
		t.Fatalf("form edit did not apply: %+v", got)
	}
	assertHostKeepsAPI(t, got)
}

func TestConfigHosts_JSONPatchPreservesAndHidesAPIToken(t *testing.T) {
	h := newAPIHarness(t)
	host := hostWithAPIToken()
	cfg := h.loadConfig()
	cfg.Client.Hosts["remote"] = host
	if err := config.Save(h.path, cfg); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{"/config/hosts", "/config/hosts/remote", "/config/schema"} {
		res := h.do(http.MethodGet, p, "", nil)
		wantStatus(t, res, http.StatusOK)
		if strings.Contains(string(res.Raw), hostTokenSentinel) || strings.Contains(string(res.Raw), `"key":"api_token"`) || strings.Contains(string(res.Raw), `"api_token":`) {
			t.Fatalf("GET %s exposes api_token: %s", p, res.Raw)
		}
	}
	wantStatus(t, h.put("/config/hosts/remote", map[string]any{"ssh": "me@elsewhere"}), http.StatusOK)
	got := h.loadConfig().Client.Hosts["remote"]
	if got.SSH != "me@elsewhere" {
		t.Fatalf("patch did not apply: %+v", got)
	}
	assertHostKeepsAPI(t, got)
}

// A secret-reference token must survive a save too (not just a literal).
func TestConfigHosts_EditPreservesSecretRefToken(t *testing.T) {
	ref := config.Secret(secretenv.Ref{Backend: secretenv.Keychain, Service: "leo-laptop"})
	cfg := &config.Config{Client: config.ClientConfig{Hosts: map[string]config.HostConfig{
		"laptop": {SSH: "me@laptop", APIURL: "http://h:1", APIToken: &ref},
	}}}
	s, dir := newTestServerWithConfigFile(t, cfg)
	w := postForm(t, s, "/web/config/host/laptop", url.Values{"ssh": {"me@x"}, "api_url": {"http://h:1"}})
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, readBody(t, w))
	}
	got := reloadTestConfig(t, strings.TrimSuffix(dir, "/leo.yaml")).Client.Hosts["laptop"]
	if got.APIToken == nil {
		t.Fatal("secret-ref api_token dropped")
	}
	if r, ok := got.APIToken.Ref(); !ok || r.Service != "leo-laptop" {
		t.Fatalf("api_token ref changed: %+v", got.APIToken)
	}
}

func TestSettingsPage_NeverRendersHostAPIToken(t *testing.T) {
	cfg := &config.Config{Client: config.ClientConfig{Hosts: map[string]config.HostConfig{"laptop": hostWithAPIToken()}}}
	s, _ := newTestServerWithConfigFile(t, cfg)
	req := httptest.NewRequest(http.MethodGet, "/config/settings", nil)
	w := httptest.NewRecorder()
	s.httpServer.Handler.ServeHTTP(w, req)
	body := readBody(t, w)
	if w.Code != http.StatusOK || !strings.Contains(body, "10.0.4.7:8370") {
		t.Fatalf("settings page: %d (api_url should render) %.200s", w.Code, body)
	}
	if strings.Contains(body, hostTokenSentinel) {
		t.Fatal("settings page leaks the host api_token")
	}
}
