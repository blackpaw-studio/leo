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

// A copy's first launch stores fork flags; any later resume built from the
// stored args must rejoin the fork, never fork again.
func TestResumeArgsStripsForkSession(t *testing.T) {
	in := []string{"--model", "opus", "--resume", "src", "--fork-session", "--session-id", "fork"}
	got := ResumeArgs(in, "fork")
	want := []string{"--model", "opus", "--resume", "fork"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResumeArgs = %v, want %v", got, want)
	}
}
