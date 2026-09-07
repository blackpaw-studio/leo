package cli

import (
	"os/exec"
	"testing"

	"github.com/blackpaw-studio/leo/internal/attachprefs"
)

func TestAttachRemoteNamedStampsLastAttachedOnSuccess(t *testing.T) {
	path := newAgentCLITestConfig(t)
	withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "attach", "scratch"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	prefs := attachprefs.Load(attachPrefsPath(homeFromConfigPath(path)))
	if _, ok := prefs.LastAttached["prod/scratch"]; !ok {
		t.Fatalf("last_attached = %v, want prod/scratch", prefs.LastAttached)
	}
}

func TestAttachRemoteNamedDoesNotStampLastAttachedOnFailure(t *testing.T) {
	path := newAgentCLITestConfig(t)
	old := agentExecCommand
	agentExecCommand = func(string, ...string) *exec.Cmd { return exec.Command("false") }
	t.Cleanup(func() { agentExecCommand = old })
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "attach", "scratch"})
	if err := root.Execute(); err == nil {
		t.Fatal("execute succeeded, want remote attach failure")
	}

	prefs := attachprefs.Load(attachPrefsPath(homeFromConfigPath(path)))
	if len(prefs.LastAttached) != 0 {
		t.Fatalf("last_attached = %v, want no stamp", prefs.LastAttached)
	}
}
