package service

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBuildClaudeShellCmdBriefArgvRoundTripsThroughRealTmux is the ephemeral-
// agent-spawn counterpart of
// internal/consult.TestClaudeBriefArgvWordRoundTripsThroughRealTmux: it
// exercises the actual code path an agent launch uses — buildClaudeShellCmd
// appending its own $(cat <path>) substitution from ProcessSpec.
// OpeningBriefPath, after every other argv element has been shell-quoted —
// into the string tmux new-session runs — against a real, throwaway tmux
// server. Every other test in this package stubs tmux, so none of them can
// catch tmux itself rejecting the command or a shell misinterpreting a quote.
// Skipped when tmux is not on PATH.
func TestBuildClaudeShellCmdBriefArgvRoundTripsThroughRealTmux(t *testing.T) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not on PATH")
	}
	// macOS caps a UNIX-domain socket path around 104 bytes; t.TempDir()
	// nests under the test name and can blow that budget on its own, well
	// before tmux's -S argument even gets appended. Use a short, dedicated
	// directory instead.
	dir, err := os.MkdirTemp("/tmp", "lo")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "s")
	t.Cleanup(func() {
		_ = exec.Command(tmuxPath, "-S", sock, "kill-server").Run()
		_ = os.RemoveAll(dir)
	})

	prompt := "line one\nline two with a $VAR, `a backtick`, a \"double\" and a 'single' quote\n" +
		strings.Repeat("the opening prompt also just needs to be large; ", 800) // ~40 KiB total
	briefPath := filepath.Join(dir, "brief.txt")
	if err := os.WriteFile(briefPath, []byte(prompt), 0o600); err != nil {
		t.Fatal(err)
	}

	// A stand-in for the claude binary: writes its last argument's exact
	// bytes to $OUTFILE, mirroring claude treating a bare positional as its
	// opening turn.
	standIn := filepath.Join(dir, "standin.sh")
	standInScript := "#!/bin/sh\nfor last in \"$@\"; do :; done\nprintf '%s' \"$last\" > \"$OUTFILE\"\n"
	if err := os.WriteFile(standIn, []byte(standInScript), 0o755); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(dir, "out.txt")

	args := []string{"--model", "sonnet"}
	spec := ProcessSpec{Name: "alpha", OpeningBriefPath: briefPath}
	cmd := buildClaudeShellCmd(standIn, args, spec, "")

	if out, err := exec.Command(tmuxPath, "-S", sock, "new-session", "-d", "-e", "OUTFILE="+outFile, cmd).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v: %s", err, out)
	}

	deadline := time.Now().Add(5 * time.Second)
	var got []byte
	for {
		got, err = os.ReadFile(outFile)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stand-in never wrote %s: %v", outFile, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !bytes.Equal(got, []byte(prompt)) {
		t.Fatalf("round-tripped opening prompt mismatch: got %d bytes, want %d bytes (equal=%v)", len(got), len(prompt), bytes.Equal(got, []byte(prompt)))
	}
}
