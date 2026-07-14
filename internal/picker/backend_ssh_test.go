package picker

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// fakeExec returns an exec seam that ignores args and emits the given stdout /
// exit code via a base64 pipe (byte-exact, survives embedded newlines).
func fakeExec(captured *[]string, stdout string, exitCode int) func(string, ...string) *exec.Cmd {
	return func(name string, args ...string) *exec.Cmd {
		if captured != nil {
			*captured = append([]string{name}, args...)
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(stdout))
		script := fmt.Sprintf("printf '%%s' '%s' | base64 -d; exit %d", encoded, exitCode)
		return exec.Command("sh", "-c", script)
	}
}

func newTestSSHBackend(exec func(string, ...string) *exec.Cmd) *SSHBackend {
	b := NewSSHBackend("hestia", "$HOME/.local/bin/leo", "tmux",
		func(tail ...string) []string {
			return append([]string{"user@hestia"}, tail...)
		})
	b.exec = exec
	return b
}

func TestSSHBackendListParsesJSON(t *testing.T) {
	jsonOut := `[{"name":"rocket","template":"assistant","status":"running"},` +
		`{"name":"blog","status":"suspended"}]`
	b := newTestSSHBackend(fakeExec(nil, jsonOut, 0))

	ags, err := b.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ags) != 2 {
		t.Fatalf("want 2 agents, got %d", len(ags))
	}
	if ags[0].Name != "rocket" || ags[0].Host != "hestia" || ags[0].Template != "assistant" {
		t.Fatalf("agent[0] = %+v", ags[0])
	}
	if ags[1].Status != "suspended" {
		t.Fatalf("agent[1].Status = %q", ags[1].Status)
	}
}

func TestSSHBackendListFallsBackToTmux(t *testing.T) {
	// First invocation (leo agent list --json) fails; List retries via tmux.
	var calls int
	b := NewSSHBackend("hestia", "$HOME/.local/bin/leo", "tmux",
		func(tail ...string) []string { return append([]string{"user@hestia"}, tail...) })
	b.exec = func(name string, args ...string) *exec.Cmd {
		calls++
		if calls == 1 {
			return exec.Command("sh", "-c", "echo 'command not found: leo' 1>&2; exit 127")
		}
		return exec.Command("sh", "-c", "printf 'leo-rocket\nleo-blog\nunrelated\n'")
	}

	ags, err := b.List(context.Background())
	if err != nil {
		t.Fatalf("List fallback: %v", err)
	}
	if len(ags) != 2 {
		t.Fatalf("want 2 leo- sessions, got %d: %+v", len(ags), ags)
	}
	for _, a := range ags {
		if !a.AttachOnly {
			t.Errorf("fallback rows must be AttachOnly: %+v", a)
		}
	}
}

func TestSSHBackendStopQuotesName(t *testing.T) {
	var captured []string
	b := newTestSSHBackend(fakeExec(&captured, "", 0))

	if err := b.Stop(context.Background(), "rocket"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	joined := strings.Join(captured, " ")
	// The remote agent name must be single-token shell-quoted so the remote
	// login shell re-parse cannot split or glob it.
	if !strings.Contains(joined, "'rocket'") {
		t.Fatalf("stop must quote the agent name; argv: %s", joined)
	}
	if !strings.Contains(joined, "agent stop") {
		t.Fatalf("stop must dispatch `agent stop`; argv: %s", joined)
	}
}

func TestSSHBackendRenameQuotesBothNames(t *testing.T) {
	var captured []string
	b := newTestSSHBackend(fakeExec(&captured, "", 0))

	if err := b.Rename(context.Background(), "rocket", "launcher"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	joined := strings.Join(captured, " ")
	if !strings.Contains(joined, "'rocket'") || !strings.Contains(joined, "'launcher'") {
		t.Fatalf("rename must quote both names; argv: %s", joined)
	}
}
