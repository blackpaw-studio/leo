package tmux

import (
	"reflect"
	"testing"
)

func TestArgsPrependsSocket(t *testing.T) {
	got := Args("new-session", "-d", "-s", "foo")
	want := []string{"-L", "leo", "new-session", "-d", "-s", "foo"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Args(...) = %v, want %v", got, want)
	}
}

func TestArgsEmpty(t *testing.T) {
	got := Args()
	want := []string{"-L", "leo"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Args() = %v, want %v", got, want)
	}
}

func TestArgsDoesNotAliasInput(t *testing.T) {
	in := []string{"kill-session", "-t", "x"}
	got := Args(in...)
	got[0] = "mutated"
	if in[0] != "kill-session" {
		t.Errorf("Args mutated caller's slice backing array; in[0] = %q", in[0])
	}
}

func TestSocketNameIsLeo(t *testing.T) {
	if SocketName != "leo" {
		t.Errorf("SocketName = %q, want %q", SocketName, "leo")
	}
}
