package codex

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

func TestArgsIncludesEffort(t *testing.T) {
	args, err := (Codex{}).Args(harness.LaunchSpec{Kind: harness.KindTask, Model: "gpt-6", Effort: "high", Workspace: "/ws", Prompt: "go", Options: Options{}})
	if err != nil || !slices.Contains(args, `model_reasoning_effort="high"`) {
		t.Fatalf("args=%q err=%v", args, err)
	}
}

func TestEffortValidationAndTOMLQuoting(t *testing.T) {
	for _, effort := range []string{"minimal", "low", "medium", "high", "xhigh"} {
		if err := (Codex{}).ValidateEffort(effort); err != nil {
			t.Fatalf("%q: %v", effort, err)
		}
	}
	for _, effort := range []string{"max", `high"`, `high\\x`} {
		if err := (Codex{}).ValidateEffort(effort); err == nil {
			t.Fatalf("accepted %q", effort)
		}
	}
	raw := `high"\\unsafe`
	args, err := (Codex{}).Args(harness.LaunchSpec{Kind: harness.KindTask, Model: "gpt-6", Effort: raw, Workspace: "/ws", Prompt: "go", Options: Options{}})
	if err != nil {
		t.Fatal(err)
	}
	for i := range args[:len(args)-1] {
		if strings.HasPrefix(args[i], "model_reasoning_effort=") {
			got, err := strconv.Unquote(strings.TrimPrefix(args[i], "model_reasoning_effort="))
			if err != nil || got != raw {
				t.Fatalf("round trip = %q, %v", got, err)
			}
			return
		}
	}
	t.Fatalf("args=%q", args)
}
