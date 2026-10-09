package leomcp

import (
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/leotools"
)

func webConfig(enabled bool, port int) *config.Config {
	return &config.Config{Web: config.WebConfig{Enabled: enabled, Port: port}}
}

func TestDispatchChildEnvAttributesTheChildAndNeverCarriesTheToken(t *testing.T) {
	got := DispatchChildEnv(webConfig(true, 9100), config.TemplateConfig{}, "d-abc123")
	want := map[string]string{
		EnvProcessName: "dispatch:d-abc123",
		EnvDispatchID:  "d-abc123",
		EnvWebPort:     "9100",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("env = %v, want %v", got, want)
	}
}

func TestDispatchChildEnvOmitsThePortWithoutADaemonListener(t *testing.T) {
	for name, cfg := range map[string]*config.Config{"web disabled": webConfig(false, 9100), "nil config": nil} {
		got := DispatchChildEnv(cfg, config.TemplateConfig{}, "d-abc123")
		if _, ok := got[EnvWebPort]; ok {
			t.Errorf("%s: web port set in %v", name, got)
		}
		if got[EnvDispatchID] != "d-abc123" {
			t.Errorf("%s: dispatch id missing from %v", name, got)
		}
	}
}

func TestDispatchChildEnvForwardsTheTemplatePermissions(t *testing.T) {
	tmpl := config.TemplateConfig{Permissions: leotools.Permissions{CanMessage: []string{"alpha"}}}
	got := DispatchChildEnv(webConfig(true, 9100), tmpl, "d-abc123")
	if got[EnvPermissions] == "" {
		t.Fatalf("restricted template lost its permissions: %v", got)
	}
	if _, ok := DispatchChildEnv(webConfig(true, 9100), config.TemplateConfig{}, "d-abc123")[EnvPermissions]; ok {
		t.Fatal("unrestricted template must not carry LEO_PERMISSIONS")
	}
}

func TestBridgeEnvNamesAddsPermissionsOnlyWhenRestricted(t *testing.T) {
	base := []string{EnvProcessName, EnvWebPort, EnvAPIToken, EnvDispatchID}
	if got := BridgeEnvNames(false); !reflect.DeepEqual(got, base) {
		t.Fatalf("unrestricted = %v", got)
	}
	if got := BridgeEnvNames(true); !reflect.DeepEqual(got, append(base, EnvPermissions)) {
		t.Fatalf("restricted = %v", got)
	}
}

func TestWithoutReservedDropsEveryTrustedVariable(t *testing.T) {
	in := []string{"PATH=/bin", "LEO_API_TOKEN=x", "LEO_WEB_PORT=1", "LEO_PROCESS_NAME=a", "LEO_PERMISSIONS={}", "LEO_DISPATCH_ID=d", "LEO_CONFIG=/c"}
	if got := WithoutReserved(in); !reflect.DeepEqual(got, []string{"PATH=/bin", "LEO_CONFIG=/c"}) {
		t.Fatalf("slice = %v", got)
	}
	got := WithoutReservedMap(map[string]string{"A": "1", "LEO_API_TOKEN": "x", "LEO_PERMISSIONS": "{}"})
	if !reflect.DeepEqual(got, map[string]string{"A": "1"}) {
		t.Fatalf("map = %v", got)
	}
}
