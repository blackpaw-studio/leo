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

// shellEnvPluginSource is the opencode plugin that blanks TMUX and TMUX_PANE
// in the shell commands an agent runs (its shell.env hook).
//
//go:embed plugin/leo-shellenv.js
var shellEnvPluginSource []byte

const (
	turnPluginPrefix     = "leo-turns"
	shellEnvPluginPrefix = "leo-shellenv"
)

// turnPluginDir is where the plugin files are written. A var so tests point
// it at a temp dir.
var turnPluginDir = func() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "leo", "opencode"), nil
}

// pluginPath is content-addressed, so an upgraded leo never rewrites a file a
// running opencode loaded from an older one.
func pluginPath(prefix string, source []byte) (string, error) {
	dir, err := turnPluginDir()
	if err != nil {
		return "", fmt.Errorf("opencode: resolving plugin dir: %w", err)
	}
	sum := sha256.Sum256(source)
	return filepath.Join(dir, fmt.Sprintf("%s-%x.js", prefix, sum[:6])), nil
}

// ensurePlugin writes the plugin file if it is missing or differs, and
// returns its path.
func ensurePlugin(prefix string, source []byte) (string, error) {
	path, err := pluginPath(prefix, source)
	if err != nil {
		return "", err
	}
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, source) { // #nosec G304 -- leo-owned cache path
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("opencode: creating plugin dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+prefix+"-*")
	if err != nil {
		return "", fmt.Errorf("opencode: writing plugin: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(source); err != nil {
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

// pluginEntries are the OPENCODE_CONFIG_CONTENT plugin entries for a launch.
// Every agent launch, dispatched ones included, gets the shell env plugin.
// A supervised (non-dispatched) agent also gets the turn plugin, which stays
// inert unless leo exported this launch's attention token, so an agent
// launched unhooked is unaffected. A write failure leaves that entry out; the
// launch's AttentionLaunch reports the same failure for the turn plugin.
func pluginEntries(spec harness.LaunchSpec) []string {
	if spec.Kind != harness.KindAgent {
		return nil
	}
	var entries []string
	if path, err := ensurePlugin(shellEnvPluginPrefix, shellEnvPluginSource); err == nil {
		entries = append(entries, "file://"+path)
	}
	if !spec.Dispatched {
		if path, err := ensurePlugin(turnPluginPrefix, turnPluginSource); err == nil {
			entries = append(entries, "file://"+path)
		}
	}
	return entries
}

// attentionLaunch backs the driver's AttentionHooker: the hooks ride in on
// the env overlay (see pluginEntries), so argv is unchanged and the
// report command is unused. It fails when the plugin cannot be installed,
// so the launch is recorded as unhooked.
func attentionLaunch(_ harness.SessionHandle, args, _ []string) ([]string, error) {
	if _, err := ensurePlugin(turnPluginPrefix, turnPluginSource); err != nil {
		return nil, err
	}
	return args, nil
}
