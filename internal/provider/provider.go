// Package provider resolves the spawn-time environment for third-party
// Anthropic-compatible endpoints. Resolution happens once per spawn and the
// resolved key lives only in memory / the launched process's environment —
// callers must never persist it (see agentstore, which stores the provider
// name instead).
package provider

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

// apiKeyCmdTimeout bounds api_key_cmd execution so a hung secret-manager call
// (e.g. `op read` waiting on a locked keychain) can't stall a spawn forever.
const apiKeyCmdTimeout = 30 * time.Second

// Test seams.
var (
	lookupEnv  = os.LookupEnv
	// runCommand executes api_key_cmd via sh -c. This is trusted operator-authored
	// config (from leo.yaml), not sanitized external input, so sh -c is appropriate.
	runCommand = func(ctx context.Context, command string) ([]byte, error) {
		return exec.CommandContext(ctx, "sh", "-c", command).Output()
	}
)

// Env returns the environment variables to inject into a spawned claude for
// the named provider: ANTHROPIC_BASE_URL and ANTHROPIC_AUTH_TOKEN. An empty
// name means "no provider" and returns (nil, nil). The resolved model is NOT
// part of this map — it always flows through the --model flag via the config
// model-resolution cascade.
func Env(cfg *config.Config, name string) (map[string]string, error) {
	if name == "" {
		return nil, nil
	}
	pc, ok := cfg.Providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %q not found in config", name)
	}
	key, err := resolveAPIKey(pc)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", name, err)
	}
	return map[string]string{
		"ANTHROPIC_BASE_URL":   pc.BaseURL,
		"ANTHROPIC_AUTH_TOKEN": key,
	}, nil
}

func resolveAPIKey(pc config.ProviderConfig) (string, error) {
	if pc.APIKeyEnv != "" {
		v, ok := lookupEnv(pc.APIKeyEnv)
		if !ok || strings.TrimSpace(v) == "" {
			return "", fmt.Errorf("api_key_env %s is not set", pc.APIKeyEnv)
		}
		return strings.TrimSpace(v), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), apiKeyCmdTimeout)
	defer cancel()
	out, err := runCommand(ctx, pc.APIKeyCmd)
	if err != nil {
		return "", fmt.Errorf("api_key_cmd failed: %w", err)
	}
	key := strings.TrimSpace(string(out))
	if key == "" {
		return "", fmt.Errorf("api_key_cmd produced empty output")
	}
	return key, nil
}
