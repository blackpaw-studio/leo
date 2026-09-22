package opencode

import (
	"slices"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

func TestArgsIncludesEffort(t *testing.T) {
	args, err := (Opencode{}).Args(harness.LaunchSpec{Kind: harness.KindTask, Model: "openai/gpt", Effort: "high", Workspace: "/ws", Prompt: "go", Options: Options{}})
	if err != nil || !slices.Contains(args, "--variant") || !slices.Contains(args, "high") {
		t.Fatalf("args=%q err=%v", args, err)
	}
}

func TestValidateEffortCharset(t *testing.T) {
	if err := (Opencode{}).ValidateEffort("high.v2_beta-1"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"has space", `quote"`, "line\nbreak"} {
		if err := (Opencode{}).ValidateEffort(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}
