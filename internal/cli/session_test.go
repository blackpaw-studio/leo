package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/session"
)

func TestNewSessionCmdSubcommands(t *testing.T) {
	var buf bytes.Buffer
	cmd := newSessionCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	for _, sub := range []string{"list", "status", "attach", "logs", "reset", "drain"} {
		if !bytes.Contains([]byte(out), []byte(sub)) {
			t.Fatalf("expected subcommand %q in help output:\n%s", sub, out)
		}
	}
}

// TestSessionReset_NonTTYWithoutYes_Errors verifies `leo session reset` refuses
// to destroy a session non-interactively without --yes, rather than silently
// killing tmux + dropping queued work.
func TestSessionReset_NonTTYWithoutYes_Errors(t *testing.T) {
	newTestConfig(t)
	withProcessTTY(t, false)
	withStubProcessStdio(t, strings.NewReader(""))
	cmd := newSessionResetCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"daily"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when non-TTY without --yes")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error should mention --yes, got %v", err)
	}
}

// TestSessionReset_YesFlag_ClearsStoredSession verifies --yes bypasses the
// prompt and performs the reset (clearing the stored session id).
func TestSessionReset_YesFlag_ClearsStoredSession(t *testing.T) {
	cfg, _ := newTestConfig(t)
	store := session.NewStore(cfg.HomePath)
	if err := store.Set("session:daily", "csid-x"); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	withProcessTTY(t, false)
	cmd := newSessionResetCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"daily", "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("reset --yes: %v", err)
	}
	if id, found, _ := store.Get("session:daily"); found && id != "" {
		t.Errorf("stored session id should be cleared after reset --yes, got %q", id)
	}
}
