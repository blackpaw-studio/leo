package leomcp

import (
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func stripFixture() *config.Config {
	return &config.Config{Web: config.WebConfig{Enabled: true}, Delegation: &config.DelegationConfig{
		Roles:         map[string]config.RoleSpec{"implement": {UseFor: "write code"}},
		ActiveProfile: "p", Profiles: map[string]config.Profile{"p": {Roles: map[string]config.RoleTarget{"implement": {Template: "hidden"}}}},
	}}
}

func TestStripDelegationBlockLeavesTheRestOfTheNudge(t *testing.T) {
	cfg := stripFixture()
	nudge := LeoNudge(cfg)
	if !strings.Contains(nudge, "Delegation roles:") {
		t.Fatalf("fixture nudge lacks the block: %q", nudge)
	}
	withoutDelegation := LeoNudge(&config.Config{Web: config.WebConfig{Enabled: true}})
	if got := StripDelegationBlock(nudge); got != withoutDelegation {
		t.Fatalf("stripped = %q, want %q", got, withoutDelegation)
	}
}

func TestStripDelegationBlockKeepsAUserPromptAfterIt(t *testing.T) {
	merged := LeoNudge(stripFixture()) + "\n\nuser instruction"
	got := StripDelegationBlock(merged)
	want := LeoNudge(&config.Config{Web: config.WebConfig{Enabled: true}}) + "\n\nuser instruction"
	if got != want {
		t.Fatalf("stripped = %q, want %q", got, want)
	}
}

func TestStripDelegationBlockWithoutABlockIsIdentity(t *testing.T) {
	for _, s := range []string{"", "plain prompt", "mentions <leo-delegation> but never closes"} {
		if got := StripDelegationBlock(s); got != s {
			t.Fatalf("StripDelegationBlock(%q) = %q", s, got)
		}
	}
}

func TestDelegationSectionSaysItOverridesInstructionFiles(t *testing.T) {
	cfg := stripFixture()
	section := DelegationSection(cfg)
	if !strings.Contains(section, "Delegation roles:") || !strings.Contains(section, "implement: write code") {
		t.Fatalf("section lacks the roles: %q", section)
	}
	if !strings.Contains(section, "overrides") || !strings.Contains(section, "CLAUDE.md") {
		t.Fatalf("section must say it overrides instruction files: %q", section)
	}
	if strings.Contains(section, "<leo-delegation>") {
		t.Fatalf("section carries the argv delimiters: %q", section)
	}
	cfg.Delegation.SetEnabled(false)
	if got := DelegationSection(cfg); got != "" {
		t.Fatalf("disabled delegation rendered %q", got)
	}
	cfg.Delegation.SetEnabled(true)
	cfg.Web.Enabled = false
	if got := DelegationSection(cfg); got != "" {
		t.Fatalf("web-disabled delegation rendered %q", got)
	}
}
