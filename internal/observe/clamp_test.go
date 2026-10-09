package observe

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClampText(t *testing.T) {
	cases := []struct {
		name  string
		clamp func(string) string
		in    string
		want  string
	}{
		{"detail strips CSI", ClampDetail, "\x1b[31mred\x1b[0m text", "red text"},
		{"detail strips OSC", ClampDetail, "\x1b]0;title\x07after", "after"},
		{"detail folds newlines", ClampDetail, "line one\nline two\r\n\tthree", "line one line two three"},
		{"detail drops other controls", ClampDetail, "a\x07b\x00c", "a b c"},
		{"detail trims", ClampDetail, "  padded  ", "padded"},
		{"preview folds newlines", ClampPreview, "Done.\n\nAll tests pass.", "Done. All tests pass."},
		{"empty stays empty", ClampPreview, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.clamp(tc.in); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClampTextCapsHugeInputInRunes(t *testing.T) {
	huge := strings.Repeat("é", 10*1024)
	cases := []struct {
		name  string
		clamp func(string) string
		max   int
	}{
		{"detail", ClampDetail, MaxActionDetail},
		{"preview", ClampPreview, MaxTurnPreview},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.clamp(huge)
			if n := utf8.RuneCountInString(got); n != tc.max || !utf8.ValidString(got) {
				t.Fatalf("got %d runes (valid=%v), want %d", n, utf8.ValidString(got), tc.max)
			}
		})
	}
}

func TestClampAttentionReason(t *testing.T) {
	long := strings.Repeat("x", 500)
	got := ClampAttentionReason(AttentionReason{Kind: AttentionReasonPermission, Tool: "Bash\n" + long, Detail: "\x1b[1mrm\x1b[0m -rf\n" + long})
	if got.Kind != AttentionReasonPermission {
		t.Fatalf("kind = %q", got.Kind)
	}
	if utf8.RuneCountInString(got.Tool) != MaxActionDetail || !strings.HasPrefix(got.Tool, "Bash x") {
		t.Fatalf("tool = %q", got.Tool)
	}
	if utf8.RuneCountInString(got.Detail) != MaxActionDetail || !strings.HasPrefix(got.Detail, "rm -rf x") {
		t.Fatalf("detail = %q", got.Detail)
	}
}

func TestFeaturesMatchContract(t *testing.T) {
	want := []string{"bridge_turns", "attention_reason", "dispatch_tree", "agent_usage", "agent_control", "dispatch_attach", "dispatch_removed", "state_seq", "attach_dispatch_placement", "dispatch_placement_live"}
	got := Features()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Features() = %v, want %v", got, want)
	}
	got[0] = "mutated"
	if Features()[0] != want[0] {
		t.Fatal("Features() must return a copy")
	}
}
