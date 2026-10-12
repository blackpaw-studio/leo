package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
)

const pushPolicyConfig = `
defaults:
  push:
    finished: false
templates:
  cron-bot:
    push:
      finished: true
      errored: false
`

// pushPolicyHome is a temp leo home with a leo.yaml and one agent per
// template record.
func pushPolicyHome(t *testing.T, yaml string, agents map[string]string) (home, cfgPath string) {
	t.Helper()
	home = t.TempDir()
	cfgPath = filepath.Join(home, "leo.yaml")
	writePushConfig(t, cfgPath, yaml)
	for name, tmpl := range agents {
		if err := agentstore.Save(home, agentstore.Record{Name: name, Template: tmpl}); err != nil {
			t.Fatal(err)
		}
	}
	return home, cfgPath
}

func writePushConfig(t *testing.T, path, yaml string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
}

func loaderFor(path string) func() (*config.Config, error) {
	return func() (*config.Config, error) { return config.Load(path) }
}

func TestPushPolicyResolvesByAgentTemplate(t *testing.T) {
	home, cfgPath := pushPolicyHome(t, pushPolicyConfig, map[string]string{
		"nightly": "cron-bot",
		"stray":   "no-such-template",
	})
	allow := newPushPolicy(loaderFor(cfgPath), home)

	tests := []struct {
		agent, state string
		want         bool
	}{
		{"nightly", "finished", true},     // template on beats defaults off
		{"nightly", "errored", false},     // template off
		{"nightly", "needs_input", true},  // unset everywhere
		{"unrecorded", "finished", false}, // no template record: defaults
		{"stray", "finished", false},      // unknown template: defaults
		{"stray", "errored", true},
	}
	for _, tt := range tests {
		if got := allow(tt.agent, tt.state); got != tt.want {
			t.Errorf("allow(%q, %q) = %v, want %v", tt.agent, tt.state, got, tt.want)
		}
	}
}

// A config edit between two transitions is honored by the same policy value:
// nothing is rebuilt or restarted.
func TestPushPolicyPicksUpConfigChangesLive(t *testing.T) {
	home, cfgPath := pushPolicyHome(t, pushPolicyConfig, nil)
	allow := newPushPolicy(loaderFor(cfgPath), home)

	if allow("a", "finished") {
		t.Fatal("finished should start suppressed")
	}
	writePushConfig(t, cfgPath, "defaults:\n  push:\n    finished: true\n")
	if !allow("a", "finished") {
		t.Fatal("the policy did not see the edited config")
	}
}

// Missing a needs-permission push is worse than an extra one, so every
// failure to decide sends.
func TestPushPolicyFailsOpen(t *testing.T) {
	t.Run("config unreadable", func(t *testing.T) {
		home, _ := pushPolicyHome(t, pushPolicyConfig, nil)
		allow := newPushPolicy(loaderFor(filepath.Join(home, "missing.yaml")), home)
		if !allow("a", "finished") {
			t.Fatal("unreadable config must send")
		}
	})
	t.Run("config malformed", func(t *testing.T) {
		home, cfgPath := pushPolicyHome(t, "defaults: [unterminated", nil)
		allow := newPushPolicy(loaderFor(cfgPath), home)
		if !allow("a", "finished") {
			t.Fatal("malformed config must send")
		}
	})
	t.Run("agent store corrupt", func(t *testing.T) {
		home, cfgPath := pushPolicyHome(t, pushPolicyConfig, nil)
		if err := os.MkdirAll(filepath.Join(home, "state"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(agentstore.FilePath(home), []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		allow := newPushPolicy(loaderFor(cfgPath), home)
		if !allow("a", "finished") {
			t.Fatal("unreadable agent store must send")
		}
	})
}
