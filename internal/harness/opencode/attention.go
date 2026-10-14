package opencode

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// turnPluginSource is the opencode plugin that relays a supervised agent's
// turn boundaries (chat.message → UserPromptSubmit; a turn's end → Stop,
// Interrupt when aborted, StopFailure when it errored) to the daemon's agent
// hook endpoint. opencode has no command hooks, so this is its equivalent of
// claude's and codex's `leo dispatch report` hooks.
//
//go:embed plugin/leo-turns.js
var turnPluginSource []byte

// turnPluginDir is where the plugin file is written. A var so tests point
// it at a temp dir.
var turnPluginDir = func() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "leo", "opencode"), nil
}

// turnPluginPath is content-addressed, so an upgraded leo never rewrites a
// file a running opencode loaded from an older one.
func turnPluginPath() (string, error) {
	dir, err := turnPluginDir()
	if err != nil {
		return "", fmt.Errorf("opencode: resolving plugin dir: %w", err)
	}
	sum := sha256.Sum256(turnPluginSource)
	return filepath.Join(dir, fmt.Sprintf("leo-turns-%x.js", sum[:6])), nil
}

// ensureTurnPlugin writes the plugin file if it is missing or differs, and
// returns its path.
func ensureTurnPlugin() (string, error) {
	path, err := turnPluginPath()
	if err != nil {
		return "", err
	}
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, turnPluginSource) { // #nosec G304 -- leo-owned cache path
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("opencode: creating plugin dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".leo-turns-*")
	if err != nil {
		return "", fmt.Errorf("opencode: writing plugin: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(turnPluginSource); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("opencode: writing plugin: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("opencode: writing plugin: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", fmt.Errorf("opencode: installing plugin: %w", err)
	}
	return path, nil
}

// turnPluginEntry is the OPENCODE_CONFIG_CONTENT plugin entry for a
// supervised agent launch ("" when none). The plugin itself stays inert
// unless leo exported this launch's attention token, so an agent launched
// unhooked is unaffected. A write failure leaves the entry out; the launch's
// AttentionLaunch reports the same failure.
func turnPluginEntry(spec harness.LaunchSpec) string {
	if spec.Kind != harness.KindAgent || spec.Dispatched {
		return ""
	}
	path, err := ensureTurnPlugin()
	if err != nil {
		return ""
	}
	return "file://" + path
}

// attentionLaunch backs the driver's AttentionHooker: the hooks ride in on
// the env overlay (see turnPluginEntry), so argv is unchanged and the
// report command is unused. It fails when the plugin cannot be installed,
// so the launch is recorded as unhooked.
func attentionLaunch(_ harness.SessionHandle, args, _ []string) ([]string, error) {
	if _, err := ensureTurnPlugin(); err != nil {
		return nil, err
	}
	return args, nil
}
