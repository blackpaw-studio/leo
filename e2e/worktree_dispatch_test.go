//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/consult"
)

func TestDispatchWorktreeThroughDaemonKeepsFakeHarnessChanges(t *testing.T) {
	s := newInteractiveE2E(t)
	repo := filepath.Join(s.ws, "repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
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
	f, err := os.OpenFile(s.cfgPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(f, "  headless:\n    harness: claude\n    model: opus\n    workspace: %s\n    env:\n      FAKECLAUDE_SCENARIO: worktree\n", repo)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	s.restartDaemon(t)
	var started consult.Started
	s.request(t, http.MethodPost, "/api/dispatch", map[string]string{"template": "headless", "prompt": "write", "cwd": repo, "isolation": "worktree"}, &started)
	entry := s.wait(t, started.ID)
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

func TestCodexSandboxCanWriteManagedWorktree(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Codex Seatbelt sandbox check is macOS-only")
	}
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex not installed")
	}
	if err := exec.Command(codex, "sandbox", "--help").Run(); err != nil {
		t.Skipf("codex sandbox unavailable: %v", err)
	}
	repo := t.TempDir()
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	runGit("init")
	runGit("config", "user.email", "leo@example.test")
	runGit("config", "user.name", "Leo Test")
	if err := os.WriteFile(filepath.Join(repo, "base"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "base")
	runGit("commit", "-m", "base")
	wt := filepath.Join(t.TempDir(), "wt")
	runGit("worktree", "add", "-b", "sandbox-check", wt, "HEAD")
	gitLink, err := os.ReadFile(filepath.Join(wt, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(string(gitLink), "gitdir: "))
	roots := "[" + strconv.Quote(filepath.Join(wt, ".agents")) + "," + strconv.Quote(gitDir) + "," + strconv.Quote(filepath.Join(repo, ".git")) + "]"
	baseArgs := []string{"sandbox", "-C", wt, "-c", `sandbox_mode="workspace-write"`, "-c", "sandbox_workspace_write.exclude_tmpdir_env_var=true", "-c", "sandbox_workspace_write.exclude_slash_tmp=true"}
	negative := exec.Command(codex, append(baseArgs, "sh", "-c", "printf x > negative.txt && git add negative.txt")...)
	if out, err := negative.CombinedOutput(); err == nil {
		t.Fatalf("codex sandbox staged without explicit metadata roots: %s", out)
	} else if strings.Contains(string(out), "--permission-profile") || strings.Contains(string(out), "default_permissions requires") {
		t.Skipf("codex sandbox has no usable permission profile: %s", strings.TrimSpace(string(out)))
	}
	cmd := exec.Command(codex, append(baseArgs, "-c", "sandbox_workspace_write.writable_roots="+roots, "sh", "-c", "printf x > sandbox.txt && git add sandbox.txt")...)
	if out, err := cmd.CombinedOutput(); err != nil {
		if strings.Contains(string(out), "--permission-profile") || strings.Contains(string(out), "default_permissions requires") {
			t.Skipf("codex sandbox has no usable permission profile: %s", strings.TrimSpace(string(out)))
		}
		t.Fatalf("codex sandbox write: %v: %s", err, out)
	}
	status := exec.Command("git", "-C", wt, "status", "--porcelain", "sandbox.txt")
	out, err := status.Output()
	if err != nil || !strings.Contains(string(out), "sandbox.txt") {
		t.Fatalf("sandbox write was not staged: %q, %v", out, err)
	}
}
