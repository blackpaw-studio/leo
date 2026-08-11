package leotools

import (
	"encoding/json"
	"testing"
)

func TestNamesAreUniqueAndNonEmpty(t *testing.T) {
	seen := map[string]bool{}
	for _, n := range Names {
		if n == "" {
			t.Fatal("Names contains an empty entry")
		}
		if seen[n] {
			t.Errorf("Names contains duplicate %q", n)
		}
		seen[n] = true
	}
	if !seen[SkillTool] {
		t.Errorf("Names must contain the undeniable %q tool", SkillTool)
	}
}

func TestIsZero(t *testing.T) {
	if !(Permissions{}).IsZero() {
		t.Error("zero Permissions should report IsZero")
	}
	if (Permissions{DenyTools: []string{"leo_clear"}}).IsZero() {
		t.Error("populated Permissions should not report IsZero")
	}
	// An explicitly empty (but non-nil) list carries no restriction, so it
	// must still count as zero — otherwise a round-tripped config would
	// start exporting LEO_PERMISSIONS for an unrestricted template.
	if !(Permissions{CanMessage: []string{}}).IsZero() {
		t.Error("empty non-nil list should still report IsZero")
	}
}

func TestDeniesTool(t *testing.T) {
	p := Permissions{DenyTools: []string{"leo_spawn_agent", "leo_stop_agent"}}
	if !p.DeniesTool("leo_spawn_agent") {
		t.Error("listed tool should be denied")
	}
	if p.DeniesTool("leo_list_agents") {
		t.Error("unlisted tool should not be denied")
	}
	if (Permissions{}).DeniesTool("leo_spawn_agent") {
		t.Error("zero Permissions should deny nothing")
	}
	// deny_tools is an exact-match list, not a glob list: a wildcard there
	// would be a footgun that silently strips the whole surface.
	if (Permissions{DenyTools: []string{"leo_*"}}).DeniesTool("leo_clear") {
		t.Error("deny_tools must not glob-match")
	}
	// leo_skill is never deniable, even if config validation is bypassed.
	if (Permissions{DenyTools: []string{SkillTool}}).DeniesTool(SkillTool) {
		t.Errorf("%s must never be deniable", SkillTool)
	}
}

func TestAllowsMessage(t *testing.T) {
	tests := []struct {
		name   string
		allow  []string
		target string
		want   bool
	}{
		{"absent list is unrestricted", nil, "anyone", true},
		{"empty list is unrestricted", []string{}, "anyone", true},
		{"exact match", []string{"rocket"}, "rocket", true},
		{"non-match rejected", []string{"rocket"}, "olympus", false},
		{"case sensitive", []string{"rocket"}, "Rocket", false},
		{"shorthand is not resolved", []string{"rocket"}, "rock", false},
		{"star glob", []string{"scout-*"}, "scout-leo", true},
		{"star glob non-match", []string{"scout-*"}, "codex-leo", false},
		{"question glob", []string{"agent-?"}, "agent-1", true},
		{"malformed glob still matches its literal", []string{"[bad"}, "[bad", true},
		{"malformed glob widens nothing", []string{"[bad"}, "badger", false},
		{"one of several", []string{"rocket", "olympus"}, "olympus", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := Permissions{CanMessage: tc.allow}
			if got := p.AllowsMessage(tc.target); got != tc.want {
				t.Errorf("AllowsMessage(%q) with %v = %v, want %v", tc.target, tc.allow, got, tc.want)
			}
		})
	}
}

func TestAllowsSpawnAndConsult(t *testing.T) {
	p := Permissions{CanSpawn: []string{"codex"}, CanConsult: []string{"fable", "opus"}}

	if !p.AllowsSpawn("codex") || p.AllowsSpawn("scout") {
		t.Error("can_spawn should allow only listed templates")
	}
	if !p.AllowsConsult("opus") || p.AllowsConsult("codex") {
		t.Error("can_consult should allow only listed templates")
	}
	// The lists are independent: restricting spawn must not restrict consult.
	unrestricted := Permissions{CanSpawn: []string{"codex"}}
	if !unrestricted.AllowsConsult("anything") {
		t.Error("an absent can_consult must stay unrestricted when can_spawn is set")
	}
}

func TestJSONRoundTrip(t *testing.T) {
	want := Permissions{
		DenyTools:  []string{"leo_spawn_agent"},
		CanMessage: []string{"rocket", "scout-*"},
		CanSpawn:   []string{"codex"},
		CanConsult: []string{"fable"},
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Permissions
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !equal(got.DenyTools, want.DenyTools) || !equal(got.CanMessage, want.CanMessage) ||
		!equal(got.CanSpawn, want.CanSpawn) || !equal(got.CanConsult, want.CanConsult) {
		t.Errorf("round trip lost data: got %+v want %+v", got, want)
	}
}

func TestHasGlob(t *testing.T) {
	for _, s := range []string{"scout-*", "agent-?", "a[bc]"} {
		if !HasGlob(s) {
			t.Errorf("HasGlob(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"rocket", "olympus", "leo-agent"} {
		if HasGlob(s) {
			t.Errorf("HasGlob(%q) = true, want false", s)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
