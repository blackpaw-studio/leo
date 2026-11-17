package consult

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestLaunchWithResumeSessionPassesResumeArgv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	cfg := &config.Config{HomePath: dir, Templates: map[string]config.TemplateConfig{"claude": {Harness: "claude", Model: "sonnet"}}}
	r := NewInteractiveRuntime("/tmp/leo.yaml", func() (*config.Config, error) { return cfg, nil }, nil, "tmux", "/opt/leo")
	var launch []string
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		if slices.Contains(args, "new-window") {
			launch = append([]string(nil), args...)
			return exec.Command("echo", "%42")
		}
		return exec.Command("true")
	}
	req := LaunchRequest{ID: "d-reopen", Template: "claude", Cwd: dir, Name: "work", Prompt: "what was my first instruction?", ResumeSessionID: "sess-abc", Reopen: true}
	if _, _, err := r.Launch(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(launch) == 0 {
		t.Fatal("new-window was never called")
	}
	command := launch[len(launch)-1]
	if !strings.Contains(command, "'--resume' 'sess-abc'") {
		t.Fatalf("launch does not resume the session: %q", command)
	}
	if strings.Contains(command, "--session-id") || strings.Contains(command, "--continue") {
		t.Fatalf("launch pins or picks another session: %q", command)
	}
	if !strings.HasSuffix(strings.TrimSpace(command), claudeBriefArgvWord(dispatchBriefPath(dir, "d-reopen"))) {
		t.Fatalf("the follow-up does not ride the brief file positional: %q", command)
	}
}

func TestBridgeFallbackRelaunchKeepsResume(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.r.SetBridge(InteractiveBridge{Hub: g.hub, Launcher: g.r.bridge.Launcher, ConnectTimeout: 30 * time.Millisecond})
	pane, _, err := g.r.Launch(context.Background(), LaunchRequest{ID: "d-rslow", Template: "claude", Caller: "orch", Cwd: g.dir, Name: "work", Prompt: "next", ResumeSessionID: "sess-abc", Reopen: true})
	if err != nil || pane != rigPane {
		t.Fatalf("Launch = %q, %v", pane, err)
	}
	bridged := g.lastCall("new-window")
	if command := bridged[len(bridged)-1]; !strings.Contains(command, "'--resume' 'sess-abc'") {
		t.Fatalf("bridged launch does not resume: %q", command)
	}
	// A resume relaunch shares the run's session: it pins none, so the
	// dispatcher binds none and the reopen guard keeps judging its hooks.
	if got, err := g.r.AwaitOpening(context.Background(), rigPane); !got.Handled || !got.Relaunched || got.SessionID != "" || err != nil {
		t.Fatalf("AwaitOpening = %+v, %v; want an unpinned relaunch", got, err)
	}
	respawn := g.lastCall("respawn-pane")
	if respawn == nil {
		t.Fatal("no legacy relaunch")
	}
	if command := respawn[len(respawn)-1]; !strings.Contains(command, "'--resume' 'sess-abc'") || strings.Contains(command, "--session-id") {
		t.Fatalf("legacy relaunch dropped the resume or pinned a session: %q", command)
	}
}

func TestLaunchWithResumeSessionCodexResumesFirst(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	var launch []string
	r := interactiveCodexRuntime(t, &config.Config{HomePath: t.TempDir(), Templates: map[string]config.TemplateConfig{"codex": {Harness: "codex"}}})
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		if slices.Contains(args, "new-window") {
			launch = append([]string(nil), args...)
			return exec.Command("echo", "%42")
		}
		return exec.Command("true")
	}
	if _, _, err := r.Launch(context.Background(), LaunchRequest{ID: "d-cx", Template: "codex", Cwd: t.TempDir(), ResumeSessionID: "01a1-thread", Reopen: true}); err != nil {
		t.Fatal(err)
	}
	command := launch[len(launch)-1]
	if !strings.Contains(command, "'codex' 'resume' '01a1-thread' ") {
		t.Fatalf("codex launch does not open with resume <id>: %q", command)
	}
}
