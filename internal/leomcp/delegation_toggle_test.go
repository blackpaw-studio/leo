package leomcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func toggleConfig() *config.Config {
	return &config.Config{Web: config.WebConfig{Enabled: true}, Delegation: &config.DelegationConfig{
		Roles:         map[string]config.RoleSpec{"implement": {UseFor: "write code"}},
		ActiveProfile: "p",
		Profiles:      map[string]config.Profile{"p": {Roles: map[string]config.RoleTarget{"implement": {Template: "t"}}}},
	}}
}

func TestLeoNudgeWithDelegationOffMatchesNoDelegation(t *testing.T) {
	cfg := toggleConfig()
	if !strings.Contains(LeoNudge(cfg), "Delegation roles:") {
		t.Fatal("precondition: enabled delegation injects its block")
	}
	cfg.Delegation.SetEnabled(false)
	without := &config.Config{Web: config.WebConfig{Enabled: true}}
	if got, want := LeoNudge(cfg), LeoNudge(without); got != want {
		t.Fatalf("nudge with delegation off differs from no delegation:\n%q\n%q", got, want)
	}
	if !strings.Contains(LeoNudge(cfg), "leo_consult") {
		t.Fatal("dispatch/consult nudge must stay when delegation is off")
	}
}

func TestOpenCodeContextDropsDelegationWhenDisabled(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg := toggleConfig()
	if err := EnsureOpenCodeContext(cfg); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "opencode", "AGENTS.md")
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "Delegation roles:") {
		t.Fatalf("precondition: block written: %v %s", err, data)
	}
	cfg.Delegation.SetEnabled(false)
	if err := EnsureOpenCodeContext(cfg); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "Delegation roles:") || !strings.Contains(string(data), "leo_consult") {
		t.Fatalf("refresh kept delegation section or lost nudge: %v %s", err, data)
	}
}
