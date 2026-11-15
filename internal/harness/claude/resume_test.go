package claude

import (
	"slices"
	"testing"
)

func TestClaudeInteractiveResumeArgs(t *testing.T) {
	args := []string{"--model", "opus", "--settings", "{}"}
	got := (Claude{}).InteractiveResumeArgs(args, "s-1")
	if want := []string{"--model", "opus", "--settings", "{}", "--resume", "s-1"}; !slices.Equal(got, want) {
		t.Fatalf("InteractiveResumeArgs = %q, want %q", got, want)
	}
	if !slices.Equal(args, []string{"--model", "opus", "--settings", "{}"}) {
		t.Fatalf("input argv mutated: %q", args)
	}
}
