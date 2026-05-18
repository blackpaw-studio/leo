package cli

import (
	"bytes"
	"testing"
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
