package provider

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func testCfg(pc config.ProviderConfig) *config.Config {
	return &config.Config{Providers: map[string]config.ProviderConfig{"glm": pc}}
}

func withSeams(t *testing.T, env map[string]string, cmdOut string, cmdErr error) {
	t.Helper()
	origLookup, origRun := lookupEnv, runCommand
	t.Cleanup(func() { lookupEnv, runCommand = origLookup, origRun })
	lookupEnv = func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	runCommand = func(ctx context.Context, cmd string) ([]byte, error) { return []byte(cmdOut), cmdErr }
}

func TestEnv(t *testing.T) {
	t.Run("empty name is a no-op", func(t *testing.T) {
		got, err := Env(testCfg(config.ProviderConfig{}), "")
		if got != nil || err != nil {
			t.Fatalf("got %v, %v; want nil, nil", got, err)
		}
	})
	t.Run("unknown provider errors", func(t *testing.T) {
		_, err := Env(testCfg(config.ProviderConfig{}), "nope")
		if err == nil || !strings.Contains(err.Error(), `provider "nope" not found`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("api_key_env resolves", func(t *testing.T) {
		withSeams(t, map[string]string{"GLM_API_KEY": " sk-abc \n"}, "", nil)
		got, err := Env(testCfg(config.ProviderConfig{BaseURL: "https://x.example", APIKeyEnv: "GLM_API_KEY"}), "glm")
		if err != nil {
			t.Fatal(err)
		}
		if got["ANTHROPIC_BASE_URL"] != "https://x.example" || got["ANTHROPIC_AUTH_TOKEN"] != "sk-abc" {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("api_key_env unset errors", func(t *testing.T) {
		withSeams(t, map[string]string{}, "", nil)
		_, err := Env(testCfg(config.ProviderConfig{BaseURL: "https://x.example", APIKeyEnv: "GLM_API_KEY"}), "glm")
		if err == nil || !strings.Contains(err.Error(), "GLM_API_KEY is not set") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("api_key_cmd resolves", func(t *testing.T) {
		withSeams(t, nil, "sk-cmd\n", nil)
		got, err := Env(testCfg(config.ProviderConfig{BaseURL: "https://x.example", APIKeyCmd: "op read x"}), "glm")
		if err != nil {
			t.Fatal(err)
		}
		if got["ANTHROPIC_AUTH_TOKEN"] != "sk-cmd" {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("api_key_cmd failure errors", func(t *testing.T) {
		withSeams(t, nil, "", fmt.Errorf("exit status 1"))
		_, err := Env(testCfg(config.ProviderConfig{BaseURL: "https://x.example", APIKeyCmd: "op read x"}), "glm")
		if err == nil || !strings.Contains(err.Error(), "api_key_cmd failed") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("api_key_cmd empty output errors", func(t *testing.T) {
		withSeams(t, nil, "  \n", nil)
		_, err := Env(testCfg(config.ProviderConfig{BaseURL: "https://x.example", APIKeyCmd: "op read x"}), "glm")
		if err == nil || !strings.Contains(err.Error(), "empty output") {
			t.Fatalf("got %v", err)
		}
	})
}
