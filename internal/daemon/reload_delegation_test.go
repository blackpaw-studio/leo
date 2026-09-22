package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestReloadConfigRefreshesOpenCodeDelegationContext(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "leo.yaml")
	cfg := &config.Config{Defaults: config.DefaultsConfig{Harness: "opencode"}, Web: config.WebConfig{Enabled: true}, Delegation: &config.DelegationConfig{Roles: map[string]config.RoleSpec{"implement": {UseFor: "write code"}}, ActiveProfile: "p", Profiles: map[string]config.Profile{"p": {Roles: map[string]config.RoleTarget{"implement": {Template: "unused"}}}}}}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	s := New(filepath.Join(dir, "leo.sock"), path, nil)
	if err := s.ReloadConfig(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "opencode", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "implement") {
		t.Fatalf("content=%q", data)
	}
}
