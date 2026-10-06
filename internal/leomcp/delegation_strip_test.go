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
	if got := StripDelegationBlock(nudge, cfg); got != withoutDelegation {
		t.Fatalf("stripped = %q, want %q", got, withoutDelegation)
	}
}

func TestStripDelegationBlockKeepsAUserPromptAfterIt(t *testing.T) {
	merged := LeoNudge(stripFixture()) + "\n\nuser instruction"
	got := StripDelegationBlock(merged, stripFixture())
	want := LeoNudge(&config.Config{Web: config.WebConfig{Enabled: true}}) + "\n\nuser instruction"
	if got != want {
		t.Fatalf("stripped = %q, want %q", got, want)
	}
}

func TestStripDelegationBlockWithoutABlockIsIdentity(t *testing.T) {
	for _, s := range []string{"", "plain prompt", "mentions <leo-delegation> but never closes"} {
		if got := StripDelegationBlock(s, stripFixture()); got != s {
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

// legacyFixture is a delegation config with a use_for that spans lines
// and even repeats the block's header.
func legacyFixture() *config.Config {
	return &config.Config{Web: config.WebConfig{Enabled: true}, Delegation: &config.DelegationConfig{
		Roles: map[string]config.RoleSpec{
			"implement": {UseFor: "write code\n\nDelegation roles:\n- not a role"},
			"review":    {},
		},
		ActiveProfile: "p", Profiles: map[string]config.Profile{"p": {}},
	}}
}

// legacyBlock is the delegation block exactly as leo rendered it into
// --append-system-prompt before the delimiters, which persisted agent
// records still carry; spelled out, not rendered, so the test pins it.
const legacyBlock = "Delegation roles:\n- implement: write code\n\nDelegation roles:\n- not a role\n- review\n" +
	"Dispatch with `leo_dispatch(role: …)`; do not pick templates or models yourself.\n" +
	"Call `leo_delegation` if a role you expect is missing.\n"

const legacyNudge = "Load leo_skill for leo operations."

func TestStripDelegationBlockRemovesAnExactLegacyBlock(t *testing.T) {
	cfg := legacyFixture()
	cases := map[string]struct{ old, want string }{
		"nudge alone":            {legacyNudge + "\n\n" + legacyBlock, legacyNudge},
		"user prompt after it":   {legacyNudge + "\n\n" + legacyBlock + "\n\nuser instruction", legacyNudge + "\n\nuser instruction"},
		"block alone, then user": {legacyBlock + "\n\nuser instruction", "user instruction"},
	}
	for name, tc := range cases {
		if got := StripDelegationBlock(tc.old, cfg); got != tc.want {
			t.Errorf("%s: stripped = %q, want %q", name, got, tc.want)
		}
	}
}

// A record persisted while delegation was on still loses its block once
// delegation is off: that is when the frozen block misleads most.
func TestStripDelegationBlockRemovesALegacyBlockAfterDelegationIsOff(t *testing.T) {
	cfg := legacyFixture()
	cfg.Delegation.SetEnabled(false)
	if got := StripDelegationBlock(legacyNudge+"\n\n"+legacyBlock, cfg); got != legacyNudge {
		t.Fatalf("stripped = %q, want %q", got, legacyNudge)
	}
}

func TestStripDelegationBlockKeepsUserTextAroundAndBetweenBlocks(t *testing.T) {
	cfg := legacyFixture()
	delimited := DelegationBlock(cfg)
	old := "before\n\n" + legacyBlock + "\n\nbetween\n\n" + delimited + "\n\nafter"
	if got, want := StripDelegationBlock(old, cfg), "before\n\nbetween\n\nafter"; got != want {
		t.Fatalf("stripped = %q, want %q", got, want)
	}
}

// Only an exact render of the current roles goes; anything else is text the
// user may have written, and stays. A record from before a roles change
// keeps its block until the agent is spawned again.
func TestStripDelegationBlockLeavesTextThatIsNotAnExactLegacyBlock(t *testing.T) {
	changed := legacyFixture()
	changed.Delegation.Roles["review"] = config.RoleSpec{UseFor: "read diffs"}
	tail := "Dispatch with `leo_dispatch(role: …)`; do not pick templates or models yourself.\n" +
		"Call `leo_delegation` if a role you expect is missing.\n"
	for name, tc := range map[string]struct {
		prompt string
		cfg    *config.Config
	}{
		"roles changed since":    {legacyNudge + "\n\n" + legacyBlock, changed},
		"no delegation config":   {legacyNudge + "\n\n" + legacyBlock, &config.Config{}},
		"nil config":             {legacyNudge + "\n\n" + legacyBlock, nil},
		"user line in the roles": {"Delegation roles:\n- implement\nKEEP THIS USER INSTRUCTION\n" + tail, legacyFixture()},
	} {
		if got := StripDelegationBlock(tc.prompt, tc.cfg); got != tc.prompt {
			t.Errorf("%s: StripDelegationBlock changed it to %q", name, got)
		}
	}
}
