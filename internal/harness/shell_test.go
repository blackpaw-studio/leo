package harness

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestShellCommandRoundTripsThroughShAndShellWords(t *testing.T) {
	argv := []string{"/a b/leo", "/O'Connor/leo", "/x'; printf INJECTED; #/leo", `/q"d$HOME/leo`, "", "plain"}
	command := ShellCommand(argv)

	out, err := exec.Command("sh", "-c", `printf '%s\0' `+command).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00"); !reflect.DeepEqual(got, argv) {
		t.Fatalf("sh argv = %q, want %q", got, argv)
	}
	if got := ShellWords(command); !reflect.DeepEqual(got, argv) {
		t.Fatalf("ShellWords = %q, want %q", got, argv)
	}
}
