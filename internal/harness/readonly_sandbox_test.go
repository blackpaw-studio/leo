package harness_test

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/harness/codex"
	"github.com/blackpaw-studio/leo/internal/harness/opencode"
)

// TestReadOnlySandboxSupport pins which harnesses can host a granted
// cross-workspace read: claude and codex enforce a read-only sandbox,
// opencode has none and must be refused.
func TestReadOnlySandboxSupport(t *testing.T) {
	cases := []struct {
		h    harness.Harness
		want bool
	}{
		{claude.Claude{}, true},
		{codex.Codex{}, true},
		{opencode.Opencode{}, false},
	}
	for _, tt := range cases {
		t.Run(tt.h.Name(), func(t *testing.T) {
			if got := harness.SupportsReadOnlySandbox(tt.h); got != tt.want {
				t.Fatalf("SupportsReadOnlySandbox(%s) = %v, want %v", tt.h.Name(), got, tt.want)
			}
		})
	}
}

func TestSecretDenylistIsTheSpecList(t *testing.T) {
	want := []string{".env", ".env.*", "*.local", "*.pem", "*.key", "id_*", ".netrc", ".npmrc", "Secrets.xcconfig"}
	got := harness.SecretDenylist()
	if len(got) != len(want) {
		t.Fatalf("SecretDenylist = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SecretDenylist = %v, want %v", got, want)
		}
	}
}
