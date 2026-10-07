package consult

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// promptCapturingDispatcher is a worktree-capable dispatcher whose fake
// harness records the argv it was launched with, so a test can assert on the
// prompt the subagent actually received.
func promptCapturingDispatcher(t *testing.T, stateDir string) (*Dispatcher, func() string) {
	t.Helper()
	d := worktreeDispatcher(t, stateDir, "")
	var mu sync.Mutex
	var argv []string
	inner := d.ExecCommandContext
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name != "ps" {
			mu.Lock()
			argv = append([]string{name}, args...)
			mu.Unlock()
		}
		return inner(ctx, name, args...)
	}
	return d, func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(argv, " ")
	}
}

func isolatedTemplateConfig() *config.Config {
	cfg := testConfig()
	cfg.Templates["reviewer"] = config.TemplateConfig{Harness: "claude", Model: "opus", Isolation: "worktree"}
	return cfg
}

func TestTemplateIsolationRunsInWorktree(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	d, launched := promptCapturingDispatcher(t, stateDir)
	started, err := d.Start(context.Background(), isolatedTemplateConfig(), Request{Template: "reviewer", Prompt: "review it", Cwd: repo})
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{started.ID}, RunTimeout)
	rec, err := d.Get(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Isolation != "worktree" || rec.Branch == "" || rec.SourceCwd != repo || rec.Cwd == repo {
		t.Fatalf("template isolation not applied: %+v", rec)
	}
	if got := launched(); !strings.Contains(got, rec.Worktree) || !strings.Contains(got, "uncommitted changes") {
		t.Fatalf("launch prompt lacks worktree notice: %q", got)
	}
}

func TestRequestIsolationAppliesWithoutTemplateIsolation(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	d, _ := promptCapturingDispatcher(t, stateDir)
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "q", Cwd: repo, Isolation: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{started.ID}, RunTimeout)
	if rec, _ := d.Get(started.ID); rec.Isolation != "worktree" {
		t.Fatalf("request isolation not applied: %+v", rec)
	}
}

func TestNonIsolatedTemplateRunsInPlaceWithoutWorktreeNotice(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	d, launched := promptCapturingDispatcher(t, stateDir)
	started, err := d.Start(context.Background(), isolatedTemplateConfig(), Request{Template: "claude", Prompt: "q", Cwd: repo})
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{started.ID}, RunTimeout)
	rec, _ := d.Get(started.ID)
	if rec.Isolation != "" || rec.Cwd != repo {
		t.Fatalf("unexpected isolation: %+v", rec)
	}
	if got := launched(); strings.Contains(got, "uncommitted changes") {
		t.Fatalf("non-isolated prompt carries worktree notice: %q", got)
	}
}

func TestConsultInheritsTemplateIsolation(t *testing.T) {
	repo, stateDir := gitTestRepo(t), t.TempDir()
	d, launched := promptCapturingDispatcher(t, stateDir)
	if _, err := d.Consult(context.Background(), isolatedTemplateConfig(), Request{Template: "reviewer", Prompt: "q", Cwd: repo}); err != nil {
		t.Fatal(err)
	}
	if got := launched(); !strings.Contains(got, "uncommitted changes") {
		t.Fatalf("consult on isolated template did not run in a worktree: %q", got)
	}
}
