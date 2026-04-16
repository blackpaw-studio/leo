package channels

import "testing"

func TestCanonicalNonEmpty(t *testing.T) {
	if len(Canonical) == 0 {
		t.Fatal("Canonical must not be empty")
	}
}

func TestCanonicalShape(t *testing.T) {
	seen := make(map[string]bool, len(Canonical))
	for _, cmd := range Canonical {
		if cmd.Name == "" {
			t.Errorf("command with empty Name: %+v", cmd)
		}
		if cmd.Description == "" {
			t.Errorf("command %q has empty Description", cmd.Name)
		}
		if cmd.Name[0] == '/' {
			t.Errorf("command %q must not include leading slash", cmd.Name)
		}
		for _, r := range cmd.Name {
			if r >= 'A' && r <= 'Z' {
				t.Errorf("command %q must be lowercase", cmd.Name)
				break
			}
		}
		if seen[cmd.Name] {
			t.Errorf("duplicate command name %q", cmd.Name)
		}
		seen[cmd.Name] = true
	}
}
