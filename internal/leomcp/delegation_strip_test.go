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

// legacyBlock is the delegation block as leo rendered it into
// --append-system-prompt before the delimiters, which persisted agent
// records still carry.
const legacyBlock = "Delegation roles:\n- implement: write code\n- review\n" +
	"Dispatch with `leo_dispatch(role: …)`; do not pick templates or models yourself.\n" +
	"Call `leo_delegation` if a role you expect is missing.\n"

func TestStripDelegationBlockRemovesTheLegacyUndelimitedBlock(t *testing.T) {
	const nudge = "Use leo_send_message to reach other agents. Load leo_skill for leo operations."
	cases := map[string]struct{ old, want string }{
		"nudge alone":            {nudge + "\n\n" + legacyBlock, nudge},
		"user prompt after it":   {nudge + "\n\n" + legacyBlock + "\n\nuser instruction", nudge + "\n\nuser instruction"},
		"block alone, then user": {legacyBlock + "\n\nuser instruction", "user instruction"},
	}
	for name, tc := range cases {
		if got := StripDelegationBlock(tc.old); got != tc.want {
			t.Errorf("%s: stripped = %q, want %q", name, got, tc.want)
		}
	}
}

func TestStripDelegationBlockKeepsTextThatOnlyResemblesTheLegacyBlock(t *testing.T) {
	for _, s := range []string{
		"my notes\n\nDelegation roles:\n- implement\nhand-written, not leo's\n",
		"inline Delegation roles:\n- implement\n" + strings.TrimPrefix(legacyBlock, "Delegation roles:\n- implement: write code\n- review\n"),
	} {
		if got := StripDelegationBlock(s); got != s {
			t.Errorf("StripDelegationBlock(%q) = %q", s, got)
		}
	}
}

// roles.*.use_for is rendered verbatim, newlines and blank lines included.
func TestStripDelegationBlockRemovesALegacyBlockWithMultilineUseFor(t *testing.T) {
	cfg := &config.Config{Web: config.WebConfig{Enabled: true}, Delegation: &config.DelegationConfig{
		Roles: map[string]config.RoleSpec{
			"implement": {UseFor: "write code\n  and its tests\n\n- not a role, still use_for"},
			"review":    {UseFor: "read diffs"},
		},
		ActiveProfile: "p", Profiles: map[string]config.Profile{"p": {}},
	}}
	block := config.RenderDelegationInstructions(cfg)
	const nudge = "Load leo_skill for leo operations."
	old := nudge + "\n\n" + block + "\n\nuser instruction"
	if got, want := StripDelegationBlock(old), nudge+"\n\nuser instruction"; got != want {
		t.Fatalf("stripped = %q, want %q", got, want)
	}
}

// User text that opens like the legacy block, ahead of the real one, stays.
func TestStripDelegationBlockKeepsALookAlikeAheadOfTheLegacyBlock(t *testing.T) {
	const lookAlike = "my notes\n\nDelegation roles:\n- implement\nhand-written, not leo's"
	old := lookAlike + "\n\n" + legacyBlock + "\n\nuser instruction"
	if got, want := StripDelegationBlock(old), lookAlike+"\n\nuser instruction"; got != want {
		t.Fatalf("stripped = %q, want %q", got, want)
	}
}
