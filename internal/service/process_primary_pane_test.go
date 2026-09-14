package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writePrimaryPaneTmux(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "tmux.log")
	statePath := filepath.Join(dir, "killed")
	optionPath := filepath.Join(dir, "primary-pane")
	scriptPath := filepath.Join(dir, "tmux")
	script := `#!/bin/sh
printf '[%s]' "$@" >> "$LEO_TEST_TMUX_LOG"
printf '\n' >> "$LEO_TEST_TMUX_LOG"
case "$3" in
new-session)
  rm -f "$LEO_TEST_TMUX_KILLED"
  printf '%%1\n'
  ;;
show-options)
  test -f "$LEO_TEST_TMUX_OPTION" && cat "$LEO_TEST_TMUX_OPTION"
  ;;
list-panes)
  printf '%%9\n'
  ;;
set-option)
  printf '%s\n' "$7" > "$LEO_TEST_TMUX_OPTION"
  ;;
display-message)
  if [ "${LEO_TEST_PRIMARY_DEAD:-0}" = 1 ]; then printf '1\n'; else printf '0\n'; fi
  ;;
kill-session)
  : > "$LEO_TEST_TMUX_KILLED"
  ;;
has-session)
  test ! -f "$LEO_TEST_TMUX_KILLED"
  ;;
esac
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LEO_TEST_TMUX_LOG", logPath)
	t.Setenv("LEO_TEST_TMUX_KILLED", statePath)
	t.Setenv("LEO_TEST_TMUX_OPTION", optionPath)
	return scriptPath, logPath
}

func waitForTmuxArgs(t *testing.T, path string, wantCount int, needle string) []string {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		out, err := os.ReadFile(path)
		if err == nil {
			lines := strings.FieldsFunc(strings.TrimSpace(string(out)), func(r rune) bool { return r == '\n' })
			count := 0
			for _, line := range lines {
				if strings.Contains(line, needle) {
					count++
				}
			}
			if count >= wantCount {
				return lines
			}
		}
		select {
		case <-deadline:
			t.Fatalf("tmux never logged %d %q calls", wantCount, needle)
		case <-time.After(time.Millisecond):
		}
	}
}

func TestPrimaryPaneDeathRestartsWithLiveSubagent(t *testing.T) {
	origPoll, origBackoff := sessionPollInterval, initialBackoff
	sessionPollInterval, initialBackoff = time.Millisecond, time.Millisecond
	defer func() { sessionPollInterval, initialBackoff = origPoll, origBackoff }()

	tmuxPath, logPath := writePrimaryPaneTmux(t)
	t.Setenv("LEO_TEST_PRIMARY_DEAD", "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sv := NewSupervisor(ctx)
	id := newProcIdentity("primary", nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		superviseProcess(ctx, tmuxPath, "false", ProcessSpec{Name: "primary", WorkDir: t.TempDir()}, t.TempDir(), sv, id)
	}()

	lines := waitForTmuxArgs(t, logPath, 2, "new-session")
	cancel()
	<-done

	firstKill, secondLaunch, launches := -1, -1, 0
	for i, line := range lines {
		if strings.Contains(line, "[kill-session][-t][=leo-primary]") && firstKill < 0 {
			firstKill = i
		}
		if strings.Contains(line, "new-session") {
			launches++
			if launches == 2 {
				secondLaunch = i
			}
		}
	}
	if firstKill < 0 || secondLaunch < 0 || firstKill > secondLaunch {
		t.Fatalf("primary pane death must kill the session before restart; tmux argv:\n%s", strings.Join(lines, "\n"))
	}
	if !containsTmuxArgvPrefix(lines, "[-L][leo][new-session][-d][-P][-F][#{pane_id}][-s][leo-primary]") {
		t.Fatalf("new-session did not request its primary pane id; tmux argv:\n%s", strings.Join(lines, "\n"))
	}
	if !containsTmuxArgv(lines, "[-L][leo][set-option][-t][=leo-primary][@leo_primary_pane][%1]") {
		t.Fatalf("new primary pane was not persisted; tmux argv:\n%s", strings.Join(lines, "\n"))
	}
}

func TestPrimaryPaneAdoptionAndRename(t *testing.T) {
	origPoll := sessionPollInterval
	sessionPollInterval = time.Millisecond
	defer func() { sessionPollInterval = origPoll }()

	tmuxPath, logPath := writePrimaryPaneTmux(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sv := NewSupervisor(ctx)
	id := newProcIdentity("old", nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		superviseProcess(ctx, tmuxPath, "false", ProcessSpec{Name: "old", WorkDir: t.TempDir(), Adopt: true}, t.TempDir(), sv, id)
	}()
	waitForTmuxArgs(t, logPath, 1, "set-option")
	id.rename("new")
	waitForTmuxArgvAfter(t, logPath,
		"[-L][leo][has-session][-t][=leo-new]",
		"[-L][leo][display-message][-p][-t][%9][#{pane_dead}]")
	cancel()
	<-done

	lines, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(lines)), "\n")
	if !containsTmuxArgv(got, "[-L][leo][show-options][-t][=leo-old][-v][@leo_primary_pane]") ||
		!containsTmuxArgv(got, "[-L][leo][list-panes][-t][=leo-old:][-F][#{pane_id}]") ||
		!containsTmuxArgv(got, "[-L][leo][set-option][-t][=leo-old][@leo_primary_pane][%9]") ||
		!containsTmuxArgv(got, "[-L][leo][display-message][-p][-t][%9][#{pane_dead}]") ||
		!containsTmuxArgv(got, "[-L][leo][has-session][-t][=leo-new]") {
		t.Fatalf("legacy adoption did not persist and follow the primary pane; tmux argv:\n%s", lines)
	}
}

func TestPrimaryPaneShutdown(t *testing.T) {
	origPoll := sessionPollInterval
	sessionPollInterval = time.Millisecond
	defer func() { sessionPollInterval = origPoll }()

	tmuxPath, logPath := writePrimaryPaneTmux(t)
	root := context.Background()
	sv := NewSupervisor(root)
	sv.tmuxPath = tmuxPath
	id := newProcIdentity("shutdown", nil)
	ctx, cancel := context.WithCancel(root)
	sv.mu.Lock()
	sv.states["shutdown"] = &ProcessState{Name: "shutdown", Status: "running", Ephemeral: true}
	sv.cancels["shutdown"] = cancel
	sv.identities["shutdown"] = id
	sv.mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		waitForSessionEnd(ctx, tmuxPath, id, ProcessSpec{}, time.Now(), nil, sv.shuttingDown)
	}()
	if err := sv.stopAgentProcess("shutdown"); err != nil {
		t.Fatal(err)
	}
	<-done

	lines, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(lines), "[kill-session][-t][=leo-shutdown]"); got != 1 {
		t.Fatalf("stop path kill-session calls = %d, want 1; tmux argv:\n%s", got, lines)
	}
}

func waitForTmuxArgvAfter(t *testing.T, path, first, then string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		out, err := os.ReadFile(path)
		if err == nil {
			seenFirst := false
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if seenFirst && line == then {
					return
				}
				if line == first {
					seenFirst = true
				}
			}
		}
		select {
		case <-deadline:
			t.Fatalf("tmux never logged argv %q followed by %q", first, then)
		case <-time.After(time.Millisecond):
		}
	}
}

func containsTmuxArgv(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}

func containsTmuxArgvPrefix(lines []string, want string) bool {
	for _, line := range lines {
		if strings.HasPrefix(line, want) {
			return true
		}
	}
	return false
}
