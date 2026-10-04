package consult

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestClaudeBriefArgvWordRoundTripsThroughRealTmux is the one non-mocked
// check in this package for the argv-brief fix: every other test here stubs
// ExecCommandContext, so none of them can catch tmux itself rejecting a
// command or a shell misinterpreting a quote. This test launches a real,
// throwaway tmux server and confirms that claudeBriefArgvWord's
// $(cat '<path>') really does expand, inside the pane's own shell, to the
// brief's exact bytes — including quotes, a dollar sign, a backtick, and
// newlines, at roughly the size a real dispatch brief reaches. Skipped when
// tmux is not on PATH.
func TestClaudeBriefArgvWordRoundTripsThroughRealTmux(t *testing.T) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not on PATH")
	}
	// macOS caps a UNIX-domain socket path around 104 bytes; t.TempDir()
	// nests under the test name and can blow that budget on its own, well
	// before tmux's -S argument even gets appended. Use a short, dedicated
	// directory instead.
	dir, err := os.MkdirTemp("/tmp", "lt")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "s")
	t.Cleanup(func() {
		_ = exec.Command(tmuxPath, "-S", sock, "kill-server").Run()
		_ = os.RemoveAll(dir)
	})

	brief := "line one\nline two with a $VAR, `a backtick`, a \"double\" and a 'single' quote\n" +
		strings.Repeat("the brief also just needs to be large; ", 1024) // ~40 KiB total
	briefPath := filepath.Join(dir, "brief.txt")
	if err := os.WriteFile(briefPath, []byte(brief), 0o600); err != nil {
		t.Fatal(err)
	}

	// A stand-in for the claude binary: writes its first argument's exact
	// bytes to $OUTFILE, renaming it into place so the poll below never reads
	// a file the shell created but printf has not filled yet (it did, under
	// load). The launch command below reproduces the same shape
	// TmuxInteractiveRuntime.Launch builds: `env KEY=VAL binary <argv...>`,
	// with claudeBriefArgvWord in the trailing positional slot.
	standIn := filepath.Join(dir, "standin.sh")
	if err := os.WriteFile(standIn, []byte("#!/bin/sh\nprintf '%s' \"$1\" > \"$OUTFILE.tmp\" && mv \"$OUTFILE.tmp\" \"$OUTFILE\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	outFile := filepath.Join(dir, "out.txt")

	command := strings.Join([]string{
		shellQuote("env"),
		shellQuote("OUTFILE=" + outFile),
		shellQuote(standIn),
		claudeBriefArgvWord(briefPath),
	}, " ")

	if out, err := exec.Command(tmuxPath, "-S", sock, "new-session", "-d", command).CombinedOutput(); err != nil {
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
	if !bytes.Equal(got, []byte(brief)) {
		t.Fatalf("round-tripped brief mismatch: got %d bytes, want %d bytes (equal=%v)", len(got), len(brief), bytes.Equal(got, []byte(brief)))
	}
}
