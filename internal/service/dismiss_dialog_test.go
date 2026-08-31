package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dialogPaneFixture is pane output carrying the confirm/cancel footer that
// both a genuine blocking startup dialog AND Claude's interactive /mcp (or
// /model, /config, ...) menu render identically. DialogKey (internal/harness/
// claude) can't tell these apart from pane text alone — that's exactly why
// dismissal must stay out of an ATTACHED session's way.
const dialogPaneFixture = "Some menu\n❯ 1. Option one\nEnter to confirm · Esc to cancel"

// writeArgvLoggingFakeTmux writes an executable script standing in for the
// tmux binary. Every invocation appends its full argv (one line) to logPath.
// It answers `list-clients` with a client line when attached is true (empty
// output otherwise) and `capture-pane` with pane content, modelling a real
// tmux server closely enough to exercise dismissStartupDialog end to end.
func writeArgvLoggingFakeTmux(t *testing.T, logPath string, attached bool, paneContent string) string {
	t.Helper()
	scriptPath := filepath.Join(t.TempDir(), "fake-tmux")
	clientsLine := ""
	if attached {
		clientsLine = "echo '/dev/ttys001 user 123'"
	}
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + shellQuote(logPath) + "\n" +
		"for arg in \"$@\"; do\n" +
		"  case \"$arg\" in\n" +
		"    list-clients)\n" +
		"      " + clientsLineOrTrue(clientsLine) + "\n" +
		"      exit 0\n" +
		"      ;;\n" +
		"    capture-pane)\n" +
		"      printf '%s' " + shellQuote(paneContent) + "\n" +
		"      exit 0\n" +
		"      ;;\n" +
		"  esac\n" +
		"done\n" +
		"exit 0\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture, needs +x
		t.Fatalf("writing fake tmux script: %v", err)
	}
	return scriptPath
}

func clientsLineOrTrue(clientsLine string) string {
	if clientsLine == "" {
		return ":"
	}
	return clientsLine
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	out, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(out)
}

// TestDismissStartupDialogSkipsWhenClientAttached proves that when a human
// has an attached tmux client on the session, dismissStartupDialog issues the
// list-clients probe but does NOT capture the pane or send any keys — even
// though the pane shows dialog chrome that would otherwise trigger an
// Escape. This is what stops leo from slamming shut a human's open /mcp menu.
func TestDismissStartupDialogSkipsWhenClientAttached(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "argv.log")
	tmuxPath := writeArgvLoggingFakeTmux(t, logPath, true, dialogPaneFixture)

	dismissStartupDialog(tmuxPath, "leo-agent-foo", "leo-agent-foo", func(pane string) string {
		if strings.Contains(pane, "Enter to confirm") && strings.Contains(pane, "Esc to cancel") {
			return "Escape"
		}
		return ""
	})

	log := readLog(t, logPath)
	if !strings.Contains(log, "list-clients -t =leo-agent-foo") {
		t.Fatalf("expected a list-clients probe in log, got:\n%s", log)
	}
	if strings.Contains(log, "capture-pane") {
		t.Fatalf("capture-pane must not run when a client is attached, got:\n%s", log)
	}
	if strings.Contains(log, "send-keys") {
		t.Fatalf("send-keys must not run when a client is attached, got:\n%s", log)
	}
}

// TestDismissStartupDialogSendsEscapeWhenUnattended proves the existing
// unattended behavior is preserved: no attached client + dialog chrome in the
// pane still results in capture-pane followed by an Escape send-keys.
func TestDismissStartupDialogSendsEscapeWhenUnattended(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "argv.log")
	tmuxPath := writeArgvLoggingFakeTmux(t, logPath, false, dialogPaneFixture)

	dismissStartupDialog(tmuxPath, "leo-agent-foo", "leo-agent-foo", func(pane string) string {
		if strings.Contains(pane, "Enter to confirm") && strings.Contains(pane, "Esc to cancel") {
			return "Escape"
		}
		return ""
	})

	log := readLog(t, logPath)
	if !strings.Contains(log, "list-clients -t =leo-agent-foo") {
		t.Fatalf("expected a list-clients probe in log, got:\n%s", log)
	}
	if !strings.Contains(log, "capture-pane") {
		t.Fatalf("expected capture-pane to run when unattended, got:\n%s", log)
	}
	if !strings.Contains(log, "send-keys") || !strings.Contains(log, "Escape") {
		t.Fatalf("expected an Escape send-keys when unattended, got:\n%s", log)
	}
}
