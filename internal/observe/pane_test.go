package observe

import "testing"

func TestSanitizePaneLineStripsAnsiAndControlChars(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "strips ansi color codes",
			in:   "\x1b[32mRunning go test ./...\x1b[0m",
			want: "Running go test ./...",
		},
		{
			name: "collapses internal whitespace",
			in:   "Running   go   test  \t ./...",
			want: "Running go test ./...",
		},
		{
			name: "strips control characters",
			in:   "hello\x07world\x7f",
			want: "helloworld",
		},
		{
			name: "trims surrounding whitespace",
			in:   "   idle   ",
			want: "idle",
		},
		{
			name: "empty input yields empty output",
			in:   "",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizePaneLine(tt.in)
			if got != tt.want {
				t.Fatalf("sanitizePaneLine(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizePaneLineTruncatesByRunes(t *testing.T) {
	// Arrange: a string of multi-byte runes longer than MaxActionDetail.
	long := ""
	for i := 0; i < MaxActionDetail+20; i++ {
		long += "é"
	}

	// Act
	got := sanitizePaneLine(long)

	// Assert: truncated by rune count, never splitting a multi-byte rune.
	runes := []rune(got)
	if len(runes) != MaxActionDetail {
		t.Fatalf("expected %d runes, got %d", MaxActionDetail, len(runes))
	}
	for _, r := range runes {
		if r != 'é' {
			t.Fatalf("truncation corrupted a rune: %q", got)
		}
	}
}

func TestLastNonEmptyLineSkipsTrailingBlankLines(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"last line has content", "one\ntwo\nthree", "three"},
		{"trailing blank lines skipped", "one\ntwo\n\n\n", "two"},
		{"only blank lines", "\n\n \n", ""},
		{"single line", "solo", "solo"},
		{"trailing carriage return trimmed", "line one\r\n", "line one"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lastNonEmptyLine(tt.in)
			if got != tt.want {
				t.Fatalf("lastNonEmptyLine(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
