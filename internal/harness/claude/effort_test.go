package claude

import (
	"slices"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

func TestArgsIncludesEffort(t *testing.T) {
	args, err := (Claude{}).Args(harness.LaunchSpec{Kind: harness.KindTask, Model: "sonnet", Effort: "high", Workspace: "/ws", Prompt: "go", Options: Options{}})
	if err != nil || !slices.Contains(args, "--effort") || !slices.Contains(args, "high") {
		t.Fatalf("args=%q err=%v", args, err)
	}
}
