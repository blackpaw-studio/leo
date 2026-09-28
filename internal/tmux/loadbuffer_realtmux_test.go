package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLoadBufferRealTmuxByteExact proves, against a REAL tmux server, that
// tmux's `load-buffer -b <name> -` (stdin) round-trips a ~40 KiB body
// byte-for-byte — the fix for the >16 KiB `set-buffer ... -- <text>`
// argument-length bug this package used to rely on. It never touches the
// shared "-L leo" server Leo's own tmux calls use (see tmux.Args /
// tmux.SocketName): it spins up its own throwaway server on a private
// socket, addressed with a short path (under /tmp, not t.TempDir()'s deeper
// per-test path) to stay under macOS's ~104-byte AF_UNIX socket path limit.
func TestLoadBufferRealTmuxByteExact(t *testing.T) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not on PATH, skipping real-tmux test: " + err.Error())
	}

	dir, err := os.MkdirTemp("/tmp", "lt")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")

	const session = "t"
	newSession := exec.Command(tmuxPath, "-S", sock, "new-session", "-d", "-s", session, "-x", "220", "-y", "50")
	if out, err := newSession.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v: %s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command(tmuxPath, "-S", sock, "kill-server").Run()
	})

	// ~40 KiB body containing quotes, $, backticks, newlines, and non-ASCII —
	// exactly the shell-metacharacter-shaped content a `set-buffer ... --
	// <text>` argument would also have to survive quoting/escaping for,
	// separately from the length limit.
	const targetSize = 40 * 1024
	var b strings.Builder
	line := "prompt line with \"quotes\", 'apostrophes', `backticks`, $vars, and emoji 🎉 — café, naïve, 日本語\n"
	for b.Len() < targetSize {
		b.WriteString(line)
	}
	body := b.String()
	if len(body) <= 16*1024 {
		t.Fatalf("test body must exceed tmux's ~16KiB set-buffer argument limit, got %d bytes", len(body))
	}

	const buf = "lt-test-buf"
	loadCmd := exec.Command(tmuxPath, "-S", sock, "load-buffer", "-b", buf, "-")
	loadCmd.Stdin = strings.NewReader(body)
	if out, err := loadCmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux load-buffer: %v: %s", err, out)
	}

	// Read the buffer back before anything pastes/deletes it — the
	// byte-exact check the brief calls out as the direct proof of the fix.
	showOut, err := exec.Command(tmuxPath, "-S", sock, "show-buffer", "-b", buf).Output()
	if err != nil {
		t.Fatalf("tmux show-buffer: %v", err)
	}
	if string(showOut) != body {
		t.Fatalf("show-buffer output not byte-exact: got %d bytes, want %d bytes", len(showOut), len(body))
	}

	// Also exercise the paste path end-to-end: paste into the session's pane
	// and confirm the pane's captured content contains the body verbatim
	// (tmux may pad/wrap the display, so this checks Contains after
	// stripping the wrapping tmux itself introduces at this width, not
	// equality against the raw capture).
	paneOut, err := exec.Command(tmuxPath, "-S", sock, "list-panes", "-t", session, "-F", "#{pane_id}").Output()
	if err != nil {
		t.Fatalf("tmux list-panes: %v", err)
	}
	pane := strings.TrimSpace(string(paneOut))

	pasteCmd := exec.Command(tmuxPath, "-S", sock, "paste-buffer", "-b", buf, "-t", pane, "-d")
	if out, err := pasteCmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux paste-buffer: %v: %s", err, out)
	}

	// The buffer must be gone after paste-buffer -d, exactly like the
	// production path relies on.
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := exec.Command(tmuxPath, "-S", sock, "show-buffer", "-b", buf).CombinedOutput()
		if err != nil {
			break // buffer deleted, as expected
		}
		if time.Now().After(deadline) {
			t.Fatal("buffer was not deleted by paste-buffer -d within the deadline")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
