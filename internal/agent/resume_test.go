package agent

import (
	"reflect"
	"testing"
)

func TestResumeArgsStripsAndAppends(t *testing.T) {
	in := []string{"--model", "sonnet", "--session-id", "old", "--name", "leo-x"}
	got := ResumeArgs(in, "new")
	want := []string{"--model", "sonnet", "--name", "leo-x", "--resume", "new"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestResumeArgsEmptySessionStripsOnly(t *testing.T) {
	in := []string{"--resume", "old", "--model", "opus"}
	got := ResumeArgs(in, "")
	want := []string{"--model", "opus"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
