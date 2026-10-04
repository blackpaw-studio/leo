package bridge

import "testing"

func TestFramedNamesTheSender(t *testing.T) {
	cases := []struct{ from, text, want string }{
		{"agent alpha", "hi there", "From agent alpha via leo:\n\nhi there"},
		{"orchestrator", "line one\nline two", "From orchestrator via leo:\n\nline one\nline two"},
		{"", "hi", "From an unnamed sender via leo:\n\nhi"},
	}
	for _, tc := range cases {
		if got := Framed(tc.from, tc.text); got != tc.want {
			t.Errorf("Framed(%q, %q) = %q, want %q", tc.from, tc.text, got, tc.want)
		}
	}
}
