package tmux

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// TestHasAttachedClientTrueWhenListClientsReturnsOutput proves a non-empty
// `list-clients` result reports the session as attached, and that the exact
// argv leo issues is `-L leo list-clients -t =<session>` — the exact-match
// target form, not a bare (prefix-matchable) session name.
func TestHasAttachedClientTrueWhenListClientsReturnsOutput(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	var gotArgs []string
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotArgs = args
		return exec.Command("printf", "%s", "/dev/ttys001 leo-user 12345\n")
	}

	if !HasAttachedClient(context.Background(), "tmux", "leo-agent-foo") {
		t.Fatal("HasAttachedClient = false, want true for non-empty list-clients output")
	}

	want := []string{"-L", "leo", "list-clients", "-t", "=leo-agent-foo"}
	if strings.Join(gotArgs, " ") != strings.Join(want, " ") {
		t.Fatalf("argv = %v, want %v", gotArgs, want)
	}
}

// TestHasAttachedClientFalseWhenListClientsEmpty proves an empty (but
// successful) list-clients result — the normal case for a session nobody has
// attached to — reports false.
func TestHasAttachedClientFalseWhenListClientsEmpty(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.Command("true")
	}

	if HasAttachedClient(context.Background(), "tmux", "leo-agent-foo") {
		t.Fatal("HasAttachedClient = true, want false for empty list-clients output")
	}
}

// TestHasAttachedClientFalseOnError proves a failing list-clients invocation
// (e.g. the session doesn't exist) fails open to false rather than false
// positive-ing an attach, since callers treat "false" as "safe to
// auto-dismiss".
func TestHasAttachedClientFalseOnError(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.Command("false")
	}

	if HasAttachedClient(context.Background(), "tmux", "leo-agent-foo") {
		t.Fatal("HasAttachedClient = true, want false when the command errors")
	}
}

// TestListClientsParsesPidAndCreated proves ListClients issues the exact
// `list-clients -F` argv against the raw session target (callers pass a
// session id like $3, which the exact-match "=" form would break) and parses
// each line into a pid and creation time, skipping malformed lines.
func TestListClientsParsesPidAndCreated(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()

	var gotArgs []string
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		gotArgs = args
		return exec.Command("printf", "%s", "4242 1760000000\nbogus\n17 notanumber\n99 1760000005\n")
	}

	got, err := ListClients(context.Background(), "tmux", "$3")
	if err != nil {
		t.Fatalf("ListClients: %v", err)
	}
	want := []string{"-L", "leo", "list-clients", "-t", "$3", "-F", "#{client_pid} #{client_created}"}
	if strings.Join(gotArgs, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv = %q, want %q", gotArgs, want)
	}
	if len(got) != 2 || got[0].PID != 4242 || got[0].Created.Unix() != 1760000000 || got[1].PID != 99 {
		t.Fatalf("clients = %+v", got)
	}
}

// TestListClientsErrorsWhenTmuxFails proves a failing list-clients surfaces
// as an error, so callers can tell "no clients" from "could not look".
func TestListClientsErrorsWhenTmuxFails(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.Command("false")
	}
	if _, err := ListClients(context.Background(), "tmux", "$3"); err == nil {
		t.Fatal("ListClients error = nil, want failure")
	}
}
