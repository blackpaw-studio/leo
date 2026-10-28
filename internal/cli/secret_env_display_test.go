package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/secretenv"
)

// fatalResolver fails the test if a display path resolves a secret.
type fatalResolver struct{ t *testing.T }

func (r fatalResolver) Resolve(secretenv.Ref) (string, error) {
	r.t.Fatal("a display path resolved a secret")
	return "", nil
}

var displayRef = secretenv.Ref{Backend: secretenv.Keychain, Service: "X"}

func saveSecretConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	cfgPath := filepath.Join(home, "leo.yaml")
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10},
		Templates: map[string]config.TemplateConfig{
			"coding": {Model: "opus", Env: config.EnvMap{"OP_TOKEN": config.Secret(displayRef), "REGION": config.Literal("us")}},
		},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	old := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = old })
}

func TestConfigShowPrintsRefMapping(t *testing.T) {
	saveSecretConfig(t)
	for _, asJSON := range []bool{false, true} {
		out := captureStdoutForConfigTests(t, func() {
			cmd := newConfigShowCmd()
			if asJSON {
				if err := cmd.Flags().Set("json", "true"); err != nil {
					t.Fatal(err)
				}
			}
			if err := cmd.RunE(cmd, nil); err != nil {
				t.Fatalf("RunE: %v", err)
			}
		})
		want := "OP_TOKEN:\n                secret: keychain\n                service: X"
		if asJSON {
			want = `"OP_TOKEN": {
          "secret": "keychain",
          "service": "X"
        }`
		}
		if !strings.Contains(out, want) {
			t.Errorf("json=%v: output lacks the ref mapping %q:\n%s", asJSON, want, out)
		}
	}
}

func TestTemplateShowDescribesRefs(t *testing.T) {
	saveSecretConfig(t)
	const desc = "<secret keychain(service=X)>"
	for _, resolved := range []bool{false, true} {
		out := captureStdout(t, func() {
			cmd := newTemplateShowCmd()
			if resolved {
				if err := cmd.Flags().Set("resolved", "true"); err != nil {
					t.Fatal(err)
				}
			}
			if err := cmd.RunE(cmd, []string{"coding"}); err != nil {
				t.Fatalf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "OP_TOKEN="+desc) || !strings.Contains(out, "REGION=us") {
			t.Errorf("resolved=%v: text output:\n%s", resolved, out)
		}
		p := runShowJSON(t, "coding", resolved)
		env, _ := p["env"].(map[string]any)
		if env["OP_TOKEN"] != desc {
			t.Errorf("resolved=%v: json env = %v", resolved, env)
		}
	}
}

func TestTaskDryRunDescribesSecretsWithoutResolving(t *testing.T) {
	cfg := &config.Config{Environments: map[string]config.EnvMap{"acct": {"OP_TOKEN": config.Secret(displayRef)}}}
	x := config.Expander{Secrets: fatalResolver{t}}
	got, err := taskDryRunEnv(cfg, config.TaskConfig{Environments: []string{"acct"}, Env: config.EnvMap{"API_TOKEN": config.Literal("sk")}}, x)
	if err != nil {
		t.Fatal(err)
	}
	want := []envPair{{key: "API_TOKEN", display: "<redacted>"}, {key: "OP_TOKEN", display: "<secret keychain(service=X)>"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("pairs = %+v, want %+v", got, want)
	}
}
