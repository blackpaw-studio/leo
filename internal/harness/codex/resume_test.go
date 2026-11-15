package codex

import (
	"slices"
	"testing"
)

func TestCodexInteractiveResumeArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"fresh argv", []string{"-a", "never", "--model", "m"}, []string{"resume", "s-1", "-a", "never", "--model", "m"}},
		{"stale resume replaced", []string{"resume", "old", "-a", "never"}, []string{"resume", "s-1", "-a", "never"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (Codex{}).InteractiveResumeArgs(c.args, "s-1"); !slices.Equal(got, c.want) {
				t.Fatalf("InteractiveResumeArgs = %q, want %q", got, c.want)
			}
		})
	}
}
