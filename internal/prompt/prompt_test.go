package prompt

import (
	"bufio"
	"strings"
	"testing"
)

func newReader(input string) *bufio.Reader {
	return bufio.NewReader(strings.NewReader(input))
}

func TestPrompt(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		defaultVal string
		want       string
	}{
		{"returns input", "hello\n", "", "hello"},
		{"returns input over default", "hello\n", "world", "hello"},
		{"returns default on empty", "\n", "world", "world"},
		{"returns empty when no default", "\n", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Prompt(newReader(tt.input), "Label", tt.defaultVal)
			if got != tt.want {
				t.Errorf("Prompt() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPromptInt(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		defaultVal int
		want       int
	}{
		{"valid int", "42\n", 0, 42},
		{"empty returns default", "\n", 10, 10},
		{"non-numeric returns zero", "abc\n", 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PromptInt(newReader(tt.input), "Label", tt.defaultVal)
			if got != tt.want {
				t.Errorf("PromptInt() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestYesNo(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		defaultYes bool
		want       bool
	}{
		{"y", "y\n", false, true},
		{"yes", "yes\n", false, true},
		{"Y uppercase", "Y\n", false, true},
		{"n", "n\n", true, false},
		{"no", "no\n", true, false},
		{"empty default yes", "\n", true, true},
		{"empty default no", "\n", false, false},
		{"random input is no", "maybe\n", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := YesNo(newReader(tt.input), "Question?", tt.defaultYes)
			if got != tt.want {
				t.Errorf("YesNo() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseChoice(t *testing.T) {
	tests := []struct {
		name string
		s    string
		max  int
		want int
	}{
		{"valid choice", "2", 3, 2},
		{"first choice", "1", 5, 1},
		{"max choice", "5", 5, 5},
		{"below range", "0", 3, 1},
		{"above range", "4", 3, 1},
		{"non-numeric", "abc", 3, 1},
		{"empty", "", 3, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseChoice(tt.s, tt.max)
			if got != tt.want {
				t.Errorf("ParseChoice(%q, %d) = %d, want %d", tt.s, tt.max, got, tt.want)
			}
		})
	}
}

func TestExpandHome(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{"tilde prefix expands", "~/Documents"},
		{"regular path unchanged", "/usr/local/bin"},
		{"empty path", ""},
		{"just tilde slash", "~/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExpandHome(tt.path)

			if strings.HasPrefix(tt.path, "~/") {
				if strings.HasPrefix(got, "~/") {
					t.Errorf("ExpandHome(%q) = %q, tilde was not expanded", tt.path, got)
				}
				if !strings.HasSuffix(got, tt.path[2:]) && tt.path != "~/" {
					t.Errorf("ExpandHome(%q) = %q, suffix not preserved", tt.path, got)
				}
			} else {
				if got != tt.path {
					t.Errorf("ExpandHome(%q) = %q, want unchanged", tt.path, got)
				}
			}
		})
	}
}
