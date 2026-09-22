package codex

import (
	"slices"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

func TestArgsIncludesEffort(t *testing.T) {
	args, err := (Codex{}).Args(harness.LaunchSpec{Kind: harness.KindTask, Model: "gpt-6", Effort: "high", Workspace: "/ws", Prompt: "go", Options: Options{}})
	if err != nil || !slices.Contains(args, `model_reasoning_effort="high"`) {
		t.Fatalf("args=%q err=%v", args, err)
	}
}
