//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

func runViewerSettingE2E(t *testing.T, session string) {
	t.Helper()
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}
	identity, err := exec.Command(realTmux, tmux.Args("new-session", "-d", "-P", "-F", "#{pane_id} #{session_id} #{window_id}", "-s", session, "sleep", "30")...).CombinedOutput()
	if err != nil {
		t.Fatalf("new-session: %v: %s", err, identity)
	}
	ids := strings.Fields(string(identity))
	if len(ids) != 3 {
		t.Fatalf("identity=%q", identity)
	}
	t.Cleanup(func() {
		_ = exec.Command(realTmux, tmux.Args("kill-session", "-t", ids[1])...).Run()
	})
	dir := t.TempDir()
	cfg := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfg, []byte("tasks: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(leoBin, "--config", cfg, "dispatch", "viewer", "set", "--session", ids[1], "placement=window")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("set: %v: %s", err, out)
	}
	out, err := exec.Command(realTmux, tmux.Args("show-options", "-v", "-t", ids[1], "@leo_viewer_placement")...).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "window" {
		t.Fatalf("show=%q err=%v", out, err)
	}
	if err := tmux.InstallViewerMenuBinding(realTmux, tmux.ViewerMenuBinding{LeoPath: leoBin, ConfigPath: cfg}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command(realTmux, tmux.Args("unbind-key", "-T", "prefix", "L")...).Run() })
	binding, err := exec.Command(realTmux, tmux.Args("list-keys", "-T", "prefix", "L")...).CombinedOutput()
	if err != nil || !strings.Contains(string(binding), "dispatch viewer menu") {
		t.Fatalf("binding=%q err=%v", binding, err)
	}
	v := consult.NewViewer(cfg, func(string) (string, bool) { return ids[1], true })
	v.TmuxPath = realTmux
	v.Executable = func() (string, error) { return leoBin, nil }
	v.Records = func() []consult.Record { return nil }
	count := func(args ...string) int {
		t.Helper()
		out, err := exec.Command(realTmux, tmux.Args(args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("count %q: %v: %s", args, err, out)
		}
		if strings.TrimSpace(string(out)) == "" {
			return 0
		}
		return len(strings.Split(strings.TrimSpace(string(out)), "\n"))
	}
	windowsBefore := count("list-windows", "-t", ids[1], "-F", "#{window_id}")
	panesBefore := count("list-panes", "-t", ids[2], "-F", "#{pane_id}")
	window := v.OnStart(consult.Record{ID: "d-menu-window", Kind: "dispatch", Status: consult.StatusQueued, Caller: "caller", CallerPaneID: ids[0], CallerSessionID: ids[1], CallerWindowID: ids[2], Template: "worker"})
	if window == "" || window[0] != '@' {
		t.Fatalf("window override returned %q", window)
	}
	if got := count("list-windows", "-t", ids[1], "-F", "#{window_id}"); got != windowsBefore+1 {
		t.Fatalf("window count=%d want %d", got, windowsBefore+1)
	}
	if got := count("list-panes", "-t", ids[2], "-F", "#{pane_id}"); got != panesBefore {
		t.Fatalf("caller pane count=%d want unchanged %d", got, panesBefore)
	}
	if out, err := exec.Command(realTmux, tmux.Args("kill-window", "-t", window)...).CombinedOutput(); err != nil {
		t.Fatalf("kill window: %v: %s", err, out)
	}
	if out, err := exec.Command(realTmux, tmux.Args("set-option", "-u", "-t", ids[1], "@leo_viewer_placement")...).CombinedOutput(); err != nil {
		t.Fatalf("unset override: %v: %s", err, out)
	}
	windowsBefore = count("list-windows", "-t", ids[1], "-F", "#{window_id}")
	panesBefore = count("list-panes", "-t", ids[2], "-F", "#{pane_id}")
	pane := v.OnStart(consult.Record{ID: "d-menu-pane", Kind: "dispatch", Status: consult.StatusQueued, Caller: "caller", CallerPaneID: ids[0], CallerSessionID: ids[1], CallerWindowID: ids[2], Template: "worker"})
	if pane == "" || pane[0] != '%' {
		t.Fatalf("default placement returned %q", pane)
	}
	if got := count("list-windows", "-t", ids[1], "-F", "#{window_id}"); got != windowsBefore {
		t.Fatalf("window count=%d want unchanged %d", got, windowsBefore)
	}
	if got := count("list-panes", "-t", ids[2], "-F", "#{pane_id}"); got != panesBefore+1 {
		t.Fatalf("caller pane count=%d want %d", got, panesBefore+1)
	}
}
func TestDispatchViewerSettingsMenu(t *testing.T) { runViewerSettingE2E(t, "settings-menu") }
func TestDispatchViewerSettingsQuotedSession(t *testing.T) {
	runViewerSettingE2E(t, fmt.Sprintf("space ' quote \" dollar$() slash\\ %d", time.Now().UnixNano()))
}

func TestDispatchViewerMenuActionLiteralSession(t *testing.T) {
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux unavailable")
	}
	holder := fmt.Sprintf("viewer-action-%d", time.Now().UnixNano())
	if out, err := exec.Command(realTmux, tmux.Args("new-session", "-d", "-s", holder, "sleep", "30")...).CombinedOutput(); err != nil {
		t.Fatalf("new-session: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command(realTmux, tmux.Args("kill-session", "-t", tmux.Target(holder))...).Run() })
	dir := t.TempDir()
	cfg := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfg, []byte("tasks: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "leo-under-test")
	capture := filepath.Join(dir, "args")
	stubScript := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + capture + "'\n"
	if err := os.WriteFile(link, []byte(stubScript), 0700); err != nil {
		t.Fatal(err)
	}
	if err := tmux.InstallViewerMenuBinding(realTmux, tmux.ViewerMenuBinding{LeoPath: link, ConfigPath: cfg}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command(realTmux, tmux.Args("unbind-key", "-T", "prefix", "L")...).Run() })
	listed, err := exec.Command(realTmux, tmux.Args("list-keys", "-T", "prefix", "L")...).CombinedOutput()
	bindingCommand := tmux.ViewerMenuBindingCommand(tmux.ViewerMenuBinding{LeoPath: link, ConfigPath: cfg})
	wantListed := "bind-key -T prefix L run-shell \"" + bindingCommand + "\""
	if err != nil || strings.TrimSpace(string(listed)) != wantListed {
		t.Fatalf("list binding=%q want=%q err=%v", strings.TrimSpace(string(listed)), wantListed, err)
	}
	if out, err := exec.Command(realTmux, tmux.Args("run-shell", "-t", holder, bindingCommand)...).CombinedOutput(); err != nil {
		t.Fatalf("run binding: %v: %s", err, out)
	}
	wantBinding := []string{"--config", cfg, "dispatch", "viewer", "menu", "--session", holder}
	if got := readArgLines(t, capture); !reflect.DeepEqual(got, wantBinding) {
		t.Fatalf("binding args=%q want=%q", got, wantBinding)
	}
	fakeTmuxDir := filepath.Join(dir, "fake-bin")
	if err := os.Mkdir(fakeTmuxDir, 0700); err != nil {
		t.Fatal(err)
	}
	menuCapture := filepath.Join(dir, "menu-argv")
	fakeTmux := filepath.Join(fakeTmuxDir, "tmux")
	fakeTmuxScript := "#!/bin/sh\nif [ \"$3\" = show-options ]; then exit 0; fi\nprintf '%s\\0' \"$@\" > '" + menuCapture + "'\n"
	if err := os.WriteFile(fakeTmux, []byte(fakeTmuxScript), 0700); err != nil {
		t.Fatal(err)
	}
	for _, session := range []string{"literal #{session_id}", "a ## b", "x #(echo y)"} {
		t.Run(session, func(t *testing.T) {
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(leoBin, link); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(link, "--config", cfg, "dispatch", "viewer", "menu", "--session", session)
			cmd.Env = replacePath(os.Environ(), fakeTmuxDir+":"+os.Getenv("PATH"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("menu: %v: %s", err, out)
			}
			raw, err := os.ReadFile(menuCapture)
			if err != nil {
				t.Fatal(err)
			}
			argvBytes := bytes.Split(bytes.TrimSuffix(raw, []byte{0}), []byte{0})
			var actions []string
			for _, arg := range argvBytes {
				if strings.HasPrefix(string(arg), "run-shell ") {
					actions = append(actions, string(arg))
				}
			}
			if len(actions) != 4 {
				t.Fatalf("actions=%q argv=%q", actions, argvBytes)
			}
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(link, []byte(stubScript), 0700); err != nil {
				t.Fatal(err)
			}
			expected := [][]string{{"--config", cfg, "dispatch", "viewer", "set", "placement=window", "--session", session}, {"--config", cfg, "dispatch", "viewer", "set", "max_panes=4", "--session", session}, {"--config", cfg, "dispatch", "viewer", "close-finished", "--session", session}, {"--config", cfg, "dispatch", "viewer", "save-default", "--session", session}}
			for i, action := range actions {
				outer := shellQuoteE2E(realTmux) + " -L leo " + action
				if out, err := exec.Command(realTmux, tmux.Args("run-shell", "-t", holder, outer)...).CombinedOutput(); err != nil {
					t.Fatalf("action %d: %v: %s", i, err, out)
				}
				if got := readArgLines(t, capture); !reflect.DeepEqual(got, expected[i]) {
					t.Fatalf("action %d args=%q want=%q; action=%q", i, got, expected[i], action)
				}
			}
		})
	}
}

func readArgLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.TrimSuffix(string(raw), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
func shellQuoteE2E(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func replacePath(env []string, path string) []string {
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, "PATH=") {
			out = append(out, entry)
		}
	}
	return append(out, "PATH="+path)
}
