package tmux

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestInstallViewerMenuBindingArgv(t *testing.T) {
	orig := serverExecCommand
	defer func() { serverExecCommand = orig }()
	var got [][]string
	serverExecCommand = func(name string, args ...string) *exec.Cmd {
		got = append(got, append([]string{name}, args...))
		return exec.Command("true")
	}
	err := InstallViewerMenuBinding("/tmux", ViewerMenuBinding{"/leo", "/cfg"})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"/tmux", "-L", "leo", "bind-key", "-T", "prefix", "L", "run-shell", "if [ '#{client_control_mode}' != 1 ]; then '/leo' --config '/cfg' dispatch viewer menu --session #{q:session_name}; fi"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("calls=%q want=%q", got, want)
	}
}

func TestInstallViewerMenuBindingRejectsEmptyExecutable(t *testing.T) {
	if err := InstallViewerMenuBinding("/tmux", ViewerMenuBinding{ConfigPath: "/cfg"}); err == nil {
		t.Fatal("want explicit empty-executable error")
	}
}

func TestViewerMenuShellQuoting(t *testing.T) {
	got := ViewerMenuBindingCommand(ViewerMenuBinding{"/a b/'leo'", "/c \\ $() #{x} \"cfg\""})
	want := "if [ '#{client_control_mode}' != 1 ]; then '/a b/'\\''leo'\\''' --config '/c \\ $() ##{x} \"cfg\"' dispatch viewer menu --session #{q:session_name}; fi"
	if got != want {
		t.Fatalf("%q != %q", got, want)
	}
}

func TestViewerMenuControlModeNoop(t *testing.T) {
	command := ViewerMenuBindingCommand(ViewerMenuBinding{"/leo", "/cfg"})
	if command[:37] != "if [ '#{client_control_mode}' != 1 ];" {
		t.Fatalf("missing control-mode guard: %q", command)
	}
}

func TestViewerMenuActionEscapesTwoFormatPasses(t *testing.T) {
	got := ViewerMenuAction("/leo", "/cfg", "literal #{session_id} a ## b x #(echo y)", "set placement=window")
	for _, want := range []string{"####{session_id}", "######## b", "####(echo y)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("action %q missing %q", got, want)
		}
	}
}
