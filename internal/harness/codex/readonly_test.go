package codex

import (
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// A granted read runs codex with --sandbox read-only whatever the template's
// permission_mode, and never grants writable_roots in the target workspace.
func TestArgsReadOnlySandboxOverridesPermissionMode(t *testing.T) {
	ws := t.TempDir()
	ro := &harness.ReadOnlySandbox{Workspaces: []string{ws}}
	shellEnv := shellEnvPolicyArgs()
	for _, mode := range []string{"", "workspace-write", "danger-full-access", permissionModeApproveForMe} {
		t.Run("mode="+mode, func(t *testing.T) {
			base := harness.LaunchSpec{Model: "gpt-5.6-sol", Workspace: ws, Dispatched: true, Prompt: "read", ReadOnly: ro,
				Options: Options{PermissionMode: mode}}

			headless := base
			headless.Kind = harness.KindTask
			got, err := Codex{}.Args(headless)
			if err != nil {
				t.Fatal(err)
			}
			want := append([]string{"exec", "--json", "--skip-git-repo-check", "--model", "gpt-5.6-sol",
				"--sandbox", "read-only", "-c", "features.multi_agent=false"}, shellEnv...)
			want = append(want, "read")
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("headless argv\n got %q\nwant %q", got, want)
			}

			resumed := headless
			resumed.Session = harness.SessionState{Mode: harness.SessionResume, ID: "thr-1"}
			got, err = Codex{}.Args(resumed)
			if err != nil {
				t.Fatal(err)
			}
			wantResume := append(append([]string(nil), want[:len(want)-1]...), "resume", "thr-1", "read")
			if !reflect.DeepEqual(got, wantResume) {
				t.Fatalf("headless resume argv\n got %q\nwant %q", got, wantResume)
			}

			tui := base
			tui.Kind = harness.KindAgent
			got, err = Codex{}.Args(tui)
			if err != nil {
				t.Fatal(err)
			}
			wantTUI := append([]string{"-a", "never", "--model", "gpt-5.6-sol",
				"--sandbox", "read-only", "-c", "features.multi_agent=false"}, shellEnv...)
			if !reflect.DeepEqual(got, wantTUI) {
				t.Fatalf("TUI argv\n got %q\nwant %q", got, wantTUI)
			}
			reopened := Codex{}.InteractiveResumeArgs(got, "thr-1")
			if !reflect.DeepEqual(reopened, append([]string{"resume", "thr-1"}, wantTUI...)) {
				t.Fatalf("TUI resume argv = %q", reopened)
			}
			if strings.Contains(strings.Join(reopened, " "), "writable_roots") {
				t.Fatalf("read-only argv grants writable_roots: %q", reopened)
			}
		})
	}
}
