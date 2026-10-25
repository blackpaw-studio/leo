package config

import (
	"os"
	"path/filepath"
	"testing"
)

// symlinkFixture returns a workspace and a sibling "outside" file that holds
// secret content; the workspace is empty.
func symlinkFixture(t *testing.T) (ws, outside string) {
	t.Helper()
	base := t.TempDir()
	ws = filepath.Join(base, "ws")
	if err := os.MkdirAll(ws, 0o750); err != nil {
		t.Fatal(err)
	}
	outside = filepath.Join(base, "operator.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	return ws, outside
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func TestPromptFile_SymlinkEscapeRefusedOnRead(t *testing.T) {
	ws, outside := symlinkFixture(t)
	mustSymlink(t, outside, filepath.Join(ws, "prompts", "task.md"))

	got, err := ReadPromptFile(ws, "prompts/task.md")
	if err == nil {
		t.Fatalf("read through escaping symlink succeeded, got %q", got)
	}
	if got != "" {
		t.Errorf("leaked content %q", got)
	}
}

func TestPromptFile_SymlinkEscapeRefusedOnWrite(t *testing.T) {
	ws, outside := symlinkFixture(t)
	mustSymlink(t, outside, filepath.Join(ws, "prompts", "task.md"))

	if err := WritePromptFile(ws, "prompts/task.md", "pwned"); err == nil {
		t.Fatal("write through escaping symlink succeeded")
	}
	if b, _ := os.ReadFile(outside); string(b) != "secret" {
		t.Errorf("outside file changed to %q", b)
	}
}

func TestPromptFile_SymlinkedDirEscapeRefused(t *testing.T) {
	ws, outside := symlinkFixture(t)
	outDir := filepath.Dir(outside)
	if err := os.WriteFile(filepath.Join(outDir, "task.md"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, outDir, filepath.Join(ws, "linked"))

	if _, err := ReadPromptFile(ws, "linked/task.md"); err == nil {
		t.Error("read through escaping directory symlink succeeded")
	}
	if err := WritePromptFile(ws, "linked/new.md", "pwned"); err == nil {
		t.Error("write through escaping directory symlink succeeded")
	}
	if _, err := os.Stat(filepath.Join(outDir, "new.md")); err == nil {
		t.Error("write created a file outside the workspace")
	}
}

func TestPromptFile_InWorkspaceSymlinkStillWorks(t *testing.T) {
	ws, _ := symlinkFixture(t)
	if err := os.MkdirAll(filepath.Join(ws, "shared"), 0o750); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(ws, "shared", "real.md")
	if err := os.WriteFile(real, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, real, filepath.Join(ws, "prompts", "task.md"))

	got, err := ReadPromptFile(ws, "prompts/task.md")
	if err != nil || got != "hello" {
		t.Fatalf("read = %q, %v", got, err)
	}
	if err := WritePromptFile(ws, "prompts/task.md", "updated"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(real); string(b) != "updated" {
		t.Errorf("target = %q, want write to land on in-workspace target", b)
	}
}

func TestPromptFile_SymlinkedWorkspaceRootStillWorks(t *testing.T) {
	ws, _ := symlinkFixture(t)
	link := filepath.Join(filepath.Dir(ws), "ws-link")
	mustSymlink(t, ws, link)

	if err := WritePromptFile(link, "p.md", "ok"); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadPromptFile(link, "p.md"); err != nil || got != "ok" {
		t.Fatalf("read = %q, %v", got, err)
	}
}

func TestPromptFile_DotDotTraversalStillRefused(t *testing.T) {
	ws, outside := symlinkFixture(t)
	rel := "../" + filepath.Base(outside)

	if _, err := ReadPromptFile(ws, rel); err == nil {
		t.Error("read with .. traversal succeeded")
	}
	if err := WritePromptFile(ws, rel, "pwned"); err == nil {
		t.Error("write with .. traversal succeeded")
	}
	if b, _ := os.ReadFile(outside); string(b) != "secret" {
		t.Errorf("outside file changed to %q", b)
	}
}

func TestListPromptFiles_ExcludesEscapingSymlinks(t *testing.T) {
	ws, outside := symlinkFixture(t)
	md := outside + ".md"
	if err := os.Rename(outside, md); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "ok.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "inner-target.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, md, filepath.Join(ws, "escape.md"))
	mustSymlink(t, filepath.Join(ws, "inner-target.md"), filepath.Join(ws, "inner.md"))

	files, err := ListPromptFiles(ws)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range files {
		got[f] = true
	}
	if got["escape.md"] {
		t.Errorf("listing includes escaping symlink: %v", files)
	}
	if !got["ok.md"] || !got["inner.md"] {
		t.Errorf("listing lost legitimate files: %v", files)
	}
}

// On a case-insensitive filesystem an absolute symlink may name the workspace
// with different capitalization than the configured path; it is still inside.
func TestPromptFile_CaseAliasedInWorkspaceSymlinkStillWorks(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "wsdir")
	if err := os.MkdirAll(filepath.Join(ws, "shared"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "WSDIR")); err != nil {
		t.Skip("filesystem is case-sensitive")
	}
	real := filepath.Join(ws, "shared", "real.md")
	if err := os.WriteFile(real, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "WSDIR", "shared", "real.md")
	mustSymlink(t, alias, filepath.Join(ws, "prompts", "task.md"))

	got, err := ReadPromptFile(ws, "prompts/task.md")
	if err != nil || got != "hello" {
		t.Fatalf("read = %q, %v", got, err)
	}
	if err := WritePromptFile(ws, "prompts/task.md", "updated"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(real); string(b) != "updated" {
		t.Errorf("target = %q", b)
	}
}

func TestListPromptFiles_SymlinkedWorkspaceRoot(t *testing.T) {
	ws, _ := symlinkFixture(t)
	if err := os.WriteFile(filepath.Join(ws, "ok.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(ws), "ws-link")
	mustSymlink(t, ws, link)

	files, err := ListPromptFiles(link)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0] != "ok.md" {
		t.Errorf("files = %v, want [ok.md]", files)
	}
}
