package claude

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// TestSystemContextMerge locks how spec.SystemContext (Leo's harness-neutral
// nudge) and Options.AppendSystemPrompt (the user-configured
// append_system_prompt harness option) combine into a single
// --append-system-prompt flag, across every launch kind that renders one.
func TestSystemContextMerge(t *testing.T) {
	const (
		nudge = "you're running under leo"
		user  = "be terse"
	)

	tests := []struct {
		name          string
		kind          harness.Kind
		systemContext string
		userPrompt    string
		wantFlag      bool
		wantValue     string
	}{
		{"agent: nudge only", harness.KindAgent, nudge, "", true, nudge},
		{"agent: nudge + user", harness.KindAgent, nudge, user, true, nudge + "\n\n" + user},
		{"agent: neither", harness.KindAgent, "", "", false, ""},

		{"task: nudge only", harness.KindTask, nudge, "", true, nudge},
		{"task: nudge + user", harness.KindTask, nudge, user, true, nudge + "\n\n" + user},
		{"task: neither", harness.KindTask, "", "", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := harness.LaunchSpec{
				Kind:          tt.kind,
				Model:         "sonnet",
				Workspace:     "/ws",
				SystemContext: tt.systemContext,
				Options: Options{
					AppendSystemPrompt: tt.userPrompt,
				},
			}
			got, err := Claude{}.Args(spec)
			if err != nil {
				t.Fatalf("Args() error = %v", err)
			}
			gotValue, gotFlag := findFlagValue(got, "--append-system-prompt")
			if gotFlag != tt.wantFlag {
				t.Fatalf("--append-system-prompt present = %v, want %v (args=%v)", gotFlag, tt.wantFlag, got)
			}
			if gotFlag && gotValue != tt.wantValue {
				t.Fatalf("--append-system-prompt value = %q, want %q", gotValue, tt.wantValue)
			}
		})
	}
}

func findFlagValue(args []string, flag string) (string, bool) {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag {
			return args[i+1], true
		}
	}
	return "", false
}
