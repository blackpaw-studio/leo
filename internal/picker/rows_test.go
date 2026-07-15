package picker

import (
	"strings"
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

func TestBuildRowsIncludesAgentsAndErrorRows(t *testing.T) {
	byHost := map[string][]Agent{
		LocalHost: {{Name: "alpha", Template: "writer", Host: LocalHost, Status: "running"}},
		"hestia":  {{Name: "rocket", Host: "hestia", Status: "suspended"}},
	}
	byHostErr := map[string]error{"down": errBoom}
	header, items := buildRows(byHost, byHostErr, map[string]struct{}{}, 0)

	// 2 agent rows + 1 error row.
	if len(items) != 3 {
		t.Fatalf("want 3 rows, got %d", len(items))
	}
	if !contains(header, "NAME") || !contains(header, "TEMPLATE") || !contains(header, "HOST") || !contains(header, "UPTIME") {
		t.Errorf("header = %q, want all column titles present", header)
	}

	var sawError, sawAlpha bool
	for _, it := range items {
		r := it.(row)
		if r.ag == nil && r.host == "down" {
			sawError = true
			if !contains(r.line, "error:") || !contains(r.line, "boom") {
				t.Errorf("error row line = %q, want it to mention the error", r.line)
			}
		}
		if r.ag != nil && r.ag.Name == "alpha" {
			sawAlpha = true
			if r.filter != "alpha writer local" {
				t.Errorf("alpha filter = %q", r.filter)
			}
			if !contains(r.line, "alpha") || !contains(r.line, "writer") || !contains(r.line, "local") {
				t.Errorf("alpha row line = %q, want name/template/host present", r.line)
			}
		}
	}
	if !sawError || !sawAlpha {
		t.Fatalf("missing rows: error=%v alpha=%v", sawError, sawAlpha)
	}
}

func TestBuildRowsErrorRowIsSingleLine(t *testing.T) {
	byHostErr := map[string]error{"hestia": errBoom}
	_, items := buildRows(nil, byHostErr, map[string]struct{}{}, 0)
	if len(items) != 1 {
		t.Fatalf("want 1 error row, got %d", len(items))
	}
	r := items[0].(row)
	if r.ag != nil {
		t.Fatalf("error row must have nil agent")
	}
	if strings.Count(r.line, "\n") != 0 {
		t.Errorf("error row line = %q, want a single line", r.line)
	}
	if !contains(r.line, "error:") {
		t.Errorf("error row line = %q, want it to contain \"error:\"", r.line)
	}
}

func TestBuildRowsSpinnerForPending(t *testing.T) {
	byHost := map[string][]Agent{
		LocalHost: {{Name: "alpha", Host: LocalHost, Status: "running"}},
	}
	pending := map[string]struct{}{rowKey(LocalHost, "alpha"): {}}
	_, items := buildRows(byHost, nil, pending, 2)
	r := items[0].(row)
	if !hasPrefix(r.line, spinnerFrames[2]) {
		t.Errorf("pending row line = %q, want spinner-prefixed", r.line)
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

func TestColumnWidthsUsesHeaderFloorAndContentMax(t *testing.T) {
	// Content shorter than the header text — widths should fall back to the
	// header's own length rather than shrinking below it.
	byHost := map[string][]Agent{
		LocalHost: {{Name: "a", Template: "b", Host: LocalHost}},
	}
	nameW, templateW, _ := columnWidths([]string{LocalHost}, byHost)
	if nameW != len(headerName) {
		t.Errorf("nameW = %d, want header floor %d", nameW, len(headerName))
	}
	if templateW != len(headerTemplate) {
		t.Errorf("templateW = %d, want header floor %d", templateW, len(headerTemplate))
	}

	// Content longer than the header text — widths should grow to fit it.
	byHost2 := map[string][]Agent{
		LocalHost: {{Name: "alphabet-soup", Template: "writer", Host: LocalHost}},
		"hestia":  {{Name: "a", Template: "b", Host: "hestia"}},
	}
	nameW2, templateW2, hostW2 := columnWidths([]string{LocalHost, "hestia"}, byHost2)
	if nameW2 != len("alphabet-soup") {
		t.Errorf("nameW2 = %d, want %d (longest name)", nameW2, len("alphabet-soup"))
	}
	if templateW2 != len(headerTemplate) {
		t.Errorf("templateW2 = %d, want header floor %d (longest template %q is shorter)", templateW2, len(headerTemplate), "writer")
	}
	if hostW2 != len("hestia") {
		t.Errorf("hostW2 = %d, want %d (longest host name)", hostW2, len("hestia"))
	}
}

func TestColumnWidthsCapsLongValues(t *testing.T) {
	longName := strings.Repeat("x", maxNameColumn+10)
	longTemplate := strings.Repeat("y", maxTemplateColumn+10)
	byHost := map[string][]Agent{
		LocalHost: {{Name: longName, Template: longTemplate, Host: LocalHost}},
	}
	nameW, templateW, _ := columnWidths([]string{LocalHost}, byHost)
	if nameW != maxNameColumn {
		t.Errorf("nameW = %d, want capped at %d", nameW, maxNameColumn)
	}
	if templateW != maxTemplateColumn {
		t.Errorf("templateW = %d, want capped at %d", templateW, maxTemplateColumn)
	}
}

func TestCellTruncatesWithEllipsis(t *testing.T) {
	got := cell("this-is-a-very-long-agent-name", 10)
	if n := len([]rune(got)); n != 10 {
		t.Fatalf("cell() = %q (rune len %d), want rune len 10", got, n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("cell() = %q, want truncated value to end with an ellipsis", got)
	}
}

func TestCellPadsShortValues(t *testing.T) {
	got := cell("hi", 5)
	if got != "hi   " {
		t.Errorf("cell() = %q, want %q", got, "hi   ")
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
