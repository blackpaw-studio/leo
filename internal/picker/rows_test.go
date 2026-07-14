package picker

import (
	"testing"
	"time"
)

func TestGlyphByStatus(t *testing.T) {
	cases := map[string]string{
		"running":    glyphRunning,
		"starting":   glyphStarting,
		"restarting": glyphStarting,
		"suspended":  glyphSuspended,
		"stopped":    glyphStopped,
		"weird":      glyphStopped, // unknown → stopped glyph
	}
	for status, want := range cases {
		if got := glyph(status); got != want {
			t.Errorf("glyph(%q) = %q, want %q", status, got, want)
		}
	}
}

func TestSortAgentsByName(t *testing.T) {
	ags := []Agent{{Name: "zulu"}, {Name: "alpha"}, {Name: "mike"}}
	sortAgents(ags)
	want := []string{"alpha", "mike", "zulu"}
	for i, a := range ags {
		if a.Name != want[i] {
			t.Fatalf("sorted[%d] = %q, want %q", i, a.Name, want[i])
		}
	}
}

func TestBuildRowsGroupsHostsAndErrorRows(t *testing.T) {
	byHost := map[string][]Agent{
		LocalHost: {{Name: "alpha", Template: "writer", Host: LocalHost, Status: "running"}},
		"hestia":  {{Name: "rocket", Host: "hestia", Status: "suspended"}},
	}
	byHostErr := map[string]error{"down": errBoom}
	items := buildRows(byHost, byHostErr, map[string]struct{}{}, 0)

	// 2 agent rows + 1 error row.
	if len(items) != 3 {
		t.Fatalf("want 3 rows, got %d", len(items))
	}

	var sawError, sawAlpha bool
	for _, it := range items {
		r := it.(row)
		if r.ag == nil && r.host == "down" {
			sawError = true
			if !contains(r.desc, "boom") {
				t.Errorf("error row desc = %q, want it to mention the error", r.desc)
			}
		}
		if r.ag != nil && r.ag.Name == "alpha" {
			sawAlpha = true
			if r.filter != "alpha writer local" {
				t.Errorf("alpha filter = %q", r.filter)
			}
		}
	}
	if !sawError || !sawAlpha {
		t.Fatalf("missing rows: error=%v alpha=%v", sawError, sawAlpha)
	}
}

func TestBuildRowsSpinnerForPending(t *testing.T) {
	byHost := map[string][]Agent{
		LocalHost: {{Name: "alpha", Host: LocalHost, Status: "running"}},
	}
	pending := map[string]struct{}{rowKey(LocalHost, "alpha"): {}}
	items := buildRows(byHost, nil, pending, 2)
	r := items[0].(row)
	if !hasPrefix(r.title, spinnerFrames[2]) {
		t.Errorf("pending row title = %q, want spinner-prefixed", r.title)
	}
}

func TestValidName(t *testing.T) {
	if validName("") || validName("bad name") || validName(" leading") {
		t.Errorf("expected invalid names to be rejected")
	}
	if !validName("auth-refactor") || !validName("agent_1") {
		t.Errorf("expected slug-like names to be accepted")
	}
}

func TestHumanDuration(t *testing.T) {
	if got := humanDuration(2*24*time.Hour + 4*time.Hour); got != "2d4h" {
		t.Errorf("humanDuration = %q, want 2d4h", got)
	}
	if got := humanDuration(3 * time.Hour); got != "3h" {
		t.Errorf("humanDuration = %q, want 3h", got)
	}
	if got := humanDuration(5 * time.Minute); got != "5m" {
		t.Errorf("humanDuration = %q, want 5m", got)
	}
}

// test helpers
var errBoom = boomError("boom")

type boomError string

func (b boomError) Error() string { return string(b) }

func contains(s, sub string) bool { return len(s) >= len(sub) && indexOf(s, sub) >= 0 }
func hasPrefix(s, p string) bool  { return len(s) >= len(p) && s[:len(p)] == p }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
