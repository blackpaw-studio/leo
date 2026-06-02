package agent

import "testing"

func TestNormalizeAgentName(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"auth-refactor", "leo-auth-refactor", false},
		{"  Auth_Refactor  ", "", true}, // underscore is not allowed
		{"leo-already-prefixed", "leo-already-prefixed", false},
		{"UPPER", "leo-upper", false},
		{"with spaces", "", true},
		{"dots.bad", "", true},
		{"colon:bad", "", true},
		{"slash/bad", "", true},
		{"", "", true},
		{"leo-", "", true}, // empty after prefix
		{"--leading", "leo-leading", false},
		{"trailing--", "leo-trailing", false},
	}
	for _, c := range cases {
		got, err := NormalizeAgentName(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("NormalizeAgentName(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeAgentName(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("NormalizeAgentName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeAgentName_LengthCap(t *testing.T) {
	long := make([]byte, 100)
	for i := range long {
		long[i] = 'a'
	}
	got, err := NormalizeAgentName(string(long))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) > 64 {
		t.Fatalf("name not capped: len=%d", len(got))
	}
}
