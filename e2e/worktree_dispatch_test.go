//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
)

func TestDispatchWorktreeKeepsFakeHarnessChangesAndReportsRecovery(t *testing.T) {
	repo := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.email", "leo@example.test"}, {"config", "user.name", "Leo Test"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-m", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	d := consult.NewDispatcher(consult.NewFileRecorder(t.TempDir()))
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", `printf isolated > fake-harness-output.txt; printf '%s\n' '{"type":"result","result":"done","is_error":false}'`)
	}
	cfg := &config.Config{Templates: map[string]config.TemplateConfig{"claude": {Harness: "claude", Model: "opus"}}}
	started, err := d.Start(context.Background(), cfg, consult.Request{Template: "claude", Prompt: "write", Cwd: repo, Isolation: "worktree"})
	if err != nil {
		t.Fatal(err)
	}
	entry := d.Wait(context.Background(), []string{started.ID}, consult.RunTimeout)[0]
	if entry.Worktree == "" || entry.Branch == "" {
		t.Fatalf("retained worktree not reported: %+v", entry)
	}
	data, err := os.ReadFile(filepath.Join(entry.Worktree, "fake-harness-output.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "isolated" {
		t.Fatalf("output = %q", data)
	}
}
