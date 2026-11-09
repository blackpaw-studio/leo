package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/blackpaw-studio/leo/internal/secretenv"
)

func tokenRef() *EnvValue {
	v := Secret(secretenv.Ref{Backend: secretenv.Keychain, Service: "leo-laptop"})
	return &v
}

func tokenLiteral(s string) *EnvValue {
	v := Literal(s)
	return &v
}

func TestValidate_ClientHostAPIFields(t *testing.T) {
	badRef := Secret(secretenv.Ref{Backend: secretenv.Keychain}) // no service
	cases := []struct {
		name    string
		hostKey string
		host    HostConfig
		wantErr string
	}{
		{"ssh only, no api fields", "laptop", HostConfig{SSH: "evan@laptop"}, ""},
		{"valid http", "laptop", HostConfig{APIURL: "http://10.0.4.7:8370", APIToken: tokenRef()}, ""},
		{"valid https with path", "laptop", HostConfig{APIURL: "https://leo.example.com/base", APIToken: tokenLiteral("t")}, ""},
		{"api_client set", "laptop", HostConfig{APIURL: "http://h:1", APIToken: tokenRef(), APIClient: "dionysus"}, ""},
		{"ftp scheme", "laptop", HostConfig{APIURL: "ftp://h", APIToken: tokenRef()}, "client.hosts.laptop.api_url"},
		{"no host", "laptop", HostConfig{APIURL: "http://", APIToken: tokenRef()}, "client.hosts.laptop.api_url"},
		{"userinfo", "laptop", HostConfig{APIURL: "http://u:p@h:1", APIToken: tokenRef()}, "must not contain credentials"},
		{"query", "laptop", HostConfig{APIURL: "http://h:1?x=1", APIToken: tokenRef()}, "client.hosts.laptop.api_url"},
		{"fragment", "laptop", HostConfig{APIURL: "http://h:1#x", APIToken: tokenRef()}, "client.hosts.laptop.api_url"},
		{"url without token", "laptop", HostConfig{APIURL: "http://h:1"}, "api_url and api_token must be set together"},
		{"token without url", "laptop", HostConfig{APIToken: tokenRef()}, "api_url and api_token must be set together"},
		{"empty literal token", "laptop", HostConfig{APIURL: "http://h:1", APIToken: tokenLiteral("")}, "api_url and api_token must be set together"},
		{"malformed secret ref", "laptop", HostConfig{APIURL: "http://h:1", APIToken: &badRef}, "client.hosts.laptop.api_token: secret:"},
		{"@ in host key", "a@b", HostConfig{APIURL: "http://h:1", APIToken: tokenRef()}, `host name "a@b" must not contain '@'`},
		{"@ in host key without api_url is fine", "a@b", HostConfig{SSH: "x"}, ""},
		{"bad api_client", "laptop", HostConfig{APIURL: "http://h:1", APIToken: tokenRef(), APIClient: "a#b"}, "client.hosts.laptop.api_client"},
		{"api_client with @", "laptop", HostConfig{APIURL: "http://h:1", APIToken: tokenRef(), APIClient: "a@b"}, "client.hosts.laptop.api_client"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Client: ClientConfig{Hosts: map[string]HostConfig{tc.hostKey: tc.host}}}
			err := cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidate_ClientHostLiteralTokenNotEchoed(t *testing.T) {
	cfg := &Config{Client: ClientConfig{Hosts: map[string]HostConfig{
		"laptop": {APIURL: "ftp://h", APIToken: tokenLiteral("s3cr3t-token")},
	}}}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "s3cr3t-token") {
		t.Fatalf("validation error leaks token: %v", err)
	}
}

func TestSecretReferences_IncludesHostAPIToken(t *testing.T) {
	cfg := &Config{Client: ClientConfig{Hosts: map[string]HostConfig{
		"laptop": {APIURL: "http://h:1", APIToken: tokenRef()},
		"lit":    {APIURL: "http://h:2", APIToken: tokenLiteral("t")},
		"ssh":    {SSH: "x"},
	}}}
	refs := cfg.SecretReferences()
	if len(refs) != 1 {
		t.Fatalf("SecretReferences() = %+v, want exactly the laptop ref", refs)
	}
	if refs[0].Scope != "client.hosts.laptop" || refs[0].Key != "api_token" {
		t.Fatalf("ref = %s/%s, want client.hosts.laptop/api_token", refs[0].Scope, refs[0].Key)
	}
}

func TestHostConfig_YAMLRoundTrip(t *testing.T) {
	const noAPI = "ssh: evan@laptop\nleo_path: /x/leo\n"
	var h HostConfig
	if err := yaml.Unmarshal([]byte(noAPI), &h); err != nil {
		t.Fatal(err)
	}
	out, err := yaml.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != noAPI {
		t.Fatalf("round trip changed a host without api fields:\n%s", out)
	}

	const withAPI = "ssh: evan@laptop\napi_url: http://10.0.4.7:8370\napi_token:\n    secret: keychain\n    service: leo-laptop\napi_client: dionysus\n"
	var h2 HostConfig
	if err := yaml.Unmarshal([]byte(withAPI), &h2); err != nil {
		t.Fatal(err)
	}
	if h2.APIToken == nil {
		t.Fatal("api_token not parsed")
	}
	if _, ok := h2.APIToken.Ref(); !ok {
		t.Fatal("api_token not parsed as a secret ref")
	}
	out2, err := yaml.Marshal(h2)
	if err != nil {
		t.Fatal(err)
	}
	if string(out2) != withAPI {
		t.Fatalf("round trip mismatch:\n%s\nwant\n%s", out2, withAPI)
	}
}
