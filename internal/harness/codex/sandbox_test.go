package codex

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGitMetaDirs(t *testing.T) {
	t.Run("ordinary checkout", func(t *testing.T) {
		// Arrange
		ws := t.TempDir()
		gitDir := filepath.Join(ws, ".git")
		if err := os.Mkdir(gitDir, 0o750); err != nil {
			t.Fatalf("Mkdir: %v", err)
		}

		// Act
		got := gitMetaDirs(ws)

		// Assert
		want := []string{resolvePath(gitDir)}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("linked worktree", func(t *testing.T) {
		// Arrange: fabricate <repo>/.git (the common dir) and
		// <repo>/.git/worktrees/wt1 (the linked worktree's real gitdir),
		// with the worktree checkout's .git file pointing at the latter
		// and a commondir file pointing back at the former.
		repo := t.TempDir()
		mainGitDir := filepath.Join(repo, ".git")
		worktreeGitDir := filepath.Join(mainGitDir, "worktrees", "wt1")
		if err := os.MkdirAll(worktreeGitDir, 0o750); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(filepath.Join(worktreeGitDir, "commondir"), []byte("../..\n"), 0o600); err != nil {
			t.Fatalf("WriteFile commondir: %v", err)
		}
		ws := t.TempDir()
		if err := os.WriteFile(filepath.Join(ws, ".git"), []byte("gitdir: "+worktreeGitDir+"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile .git: %v", err)
		}

		// Act
		got := gitMetaDirs(ws)

		// Assert
		want := []string{resolvePath(worktreeGitDir), resolvePath(mainGitDir)}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("relative gitdir pointer path", func(t *testing.T) {
		// Arrange
		ws := t.TempDir()
		realGitDir := filepath.Join(ws, "realgit")
		if err := os.Mkdir(realGitDir, 0o750); err != nil {
			t.Fatalf("Mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(ws, ".git"), []byte("gitdir: realgit\n"), 0o600); err != nil {
			t.Fatalf("WriteFile .git: %v", err)
		}

		// Act
		got := gitMetaDirs(ws)

		// Assert
		want := []string{resolvePath(realGitDir)}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("worktree pointer without commondir file", func(t *testing.T) {
		// Arrange: a .git pointer file whose gitdir has no commondir file
		// (submodule-style layout) — must fall back to just the gitdir.
		gitDir := t.TempDir()
		ws := t.TempDir()
		if err := os.WriteFile(filepath.Join(ws, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile .git: %v", err)
		}

		// Act
		got := gitMetaDirs(ws)

		// Assert
		want := []string{resolvePath(gitDir)}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("not a repo", func(t *testing.T) {
		// Arrange
		ws := t.TempDir()

		// Act
		got := gitMetaDirs(ws)

		// Assert
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	t.Run("garbage git file content", func(t *testing.T) {
		// Arrange
		ws := t.TempDir()
		if err := os.WriteFile(filepath.Join(ws, ".git"), []byte("not a gitdir pointer\n"), 0o600); err != nil {
			t.Fatalf("WriteFile .git: %v", err)
		}

		// Act
		got := gitMetaDirs(ws)

		// Assert
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
}

// TestResolvePath uses stdlib filepath.EvalSymlinks on the *existing*
// portion of each path as an independent oracle, so the walk-up fallback
// for not-yet-created leaves is verified against ground truth rather than
// against resolvePath itself.
func TestResolvePath(t *testing.T) {
	// Arrange: base/target (real dir) and base/link -> target.
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o750); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"existing symlinked dir", link, resolvedTarget},
		{"missing leaf under symlinked parent", filepath.Join(link, "newdir"), filepath.Join(resolvedTarget, "newdir")},
		{"multi-level missing suffix", filepath.Join(link, "a", "b"), filepath.Join(resolvedTarget, "a", "b")},
		{"already-resolved existing path", resolvedTarget, resolvedTarget},
		{"missing leaf under resolved parent", filepath.Join(resolvedTarget, "missing"), filepath.Join(resolvedTarget, "missing")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := resolvePath(tt.in)

			// Assert
			if got != tt.want {
				t.Errorf("resolvePath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSandboxWritableRootsArgs(t *testing.T) {
	t.Run("non-repo workspace still gets .agents root", func(t *testing.T) {
		// Arrange
		ws := t.TempDir()

		// Act
		got := sandboxWritableRootsArgs(ws)

		// Assert
		want := []string{"-c", `sandbox_workspace_write.writable_roots=["` + resolvePath(filepath.Join(ws, ".agents")) + `"]`}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("git checkout adds .git root after .agents", func(t *testing.T) {
		// Arrange
		ws := t.TempDir()
		gitDir := filepath.Join(ws, ".git")
		if err := os.Mkdir(gitDir, 0o750); err != nil {
			t.Fatalf("Mkdir: %v", err)
		}

		// Act
		got := sandboxWritableRootsArgs(ws)

		// Assert
		want := []string{"-c", `sandbox_workspace_write.writable_roots=["` +
			resolvePath(filepath.Join(ws, ".agents")) + `","` + resolvePath(gitDir) + `"]`}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("empty workspace returns nil", func(t *testing.T) {
		// Act
		got := sandboxWritableRootsArgs("")

		// Assert
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
}
