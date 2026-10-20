package web

import (
	"os/exec"
	"reflect"
	"testing"
)

const managedTmuxForTest = "/managed/tmux/3.6a/tmux"

// A daemon handed the managed tmux runs every lookup through it, so the web
// layer talks to the same server version as the supervisor.
func TestResolveDispatchCallerRunsTheInjectedTmux(t *testing.T) {
	s, _ := newTestServer(t)
	s.tmuxBin = managedTmuxForTest
	type call struct {
		bin  string
		args []string
	}
	var calls []call
	s.execCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, call{name, args})
		if len(args) > 2 && args[2] == "display-message" {
			return exec.Command("printf", "%s", "leo-assistant $1 @2")
		}
		return exec.Command("printf", "%s", "%7")
	}

	if _, err := s.resolveDispatchCaller("assistant", "", ""); err != nil {
		t.Fatalf("resolveDispatchCaller: %v", err)
	}

	wantArgs := [][]string{
		{"-L", "leo", "show-options", "-t", "=leo-assistant:", "-v", "@leo_primary_pane"},
		{"-L", "leo", "display-message", "-p", "-t", "%7", "#{session_name} #{session_id} #{window_id}"},
	}
	if len(calls) != len(wantArgs) {
		t.Fatalf("got %d tmux calls %v, want %d", len(calls), calls, len(wantArgs))
	}
	for i, c := range calls {
		if c.bin != managedTmuxForTest {
			t.Errorf("call %d ran %q, want %q", i, c.bin, managedTmuxForTest)
		}
		if !reflect.DeepEqual(c.args, wantArgs[i]) {
			t.Errorf("call %d argv = %v, want %v", i, c.args, wantArgs[i])
		}
	}
}

func TestNewTakesTheTmuxPathFromOptions(t *testing.T) {
	s, _ := newTestServer(t)
	if got := s.tmuxPath(); got == managedTmuxForTest {
		t.Fatalf("precondition: default tmuxPath() = %q", got)
	}
	s.tmuxBin = managedTmuxForTest
	if got := s.tmuxPath(); got != managedTmuxForTest {
		t.Errorf("tmuxPath() = %q, want %q", got, managedTmuxForTest)
	}
}
