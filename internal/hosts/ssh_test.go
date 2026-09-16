package hosts

import (
	"reflect"
	"testing"
)

func TestDiscoverSocketArgv(t *testing.T) {
	got := discoverArgs("u@h", []string{"-p", "2"}, "/c")
	want := []string{"u@h", "-p", "2", "-o", "ControlMaster=auto", "-o", "ControlPath=/c", "-o", "BatchMode=yes", "sh", "-c", "'" + remoteSockExpr + "'"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
}
func TestForwardArgv(t *testing.T) {
	got := forwardArgs("u@h", []string{"-p", "2"}, "/c", "/l", "/r")
	want := []string{"-N", "-T", "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes", "-o", "StreamLocalBindUnlink=yes", "-o", "ControlMaster=auto", "-o", "ControlPath=/c", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "-p", "2", "-L", "/l:/r", "u@h"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
}
func TestExitArgv(t *testing.T) {
	got := exitArgs("u@h", []string{"-p", "2"}, "/c")
	want := []string{"-o", "BatchMode=yes", "-o", "ControlPath=/c", "-O", "exit", "-p", "2", "u@h"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
}
