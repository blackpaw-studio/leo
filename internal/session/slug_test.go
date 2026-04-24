package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProjectSlug(t *testing.T) {
	tests := []struct {
		name string
		cwd  string
		want string
	}{
		{"absolute path with dotfile", "/Users/alice/.leo/workspace", "-Users-alice--leo-workspace"},
		{"absolute path no dots", "/Users/alice/Developer/everything-claude-code", "-Users-alice-Developer-everything-claude-code"},
		{"dotted repo", "/Users/alice/.leo/agents/leo", "-Users-alice--leo-agents-leo"},
		{"root", "/", "-"},
		{"private tmp", "/private/tmp", "-private-tmp"},
		{"empty", "", ""},
		{"trailing slash", "/Users/alice/", "-Users-alice-"},
		{"multiple dots", "/a/b.c.d/e", "-a-b-c-d-e"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ProjectSlug(tt.cwd); got != tt.want {
				t.Errorf("ProjectSlug(%q) = %q, want %q", tt.cwd, got, tt.want)
			}
		})
	}
}

func TestJSONLPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	got, err := JSONLPath("/Users/alice/.leo/workspace", "abc-123")
	if err != nil {
		t.Fatalf("JSONLPath: %v", err)
	}
	want := filepath.Join(home, ".claude", "projects", "-Users-alice--leo-workspace", "abc-123.jsonl")
	if got != want {
		t.Errorf("JSONLPath = %q, want %q", got, want)
	}
}

// TestIsResumeStale exercises the staleness decision by creating a jsonl at the
// real ~/.claude/projects/<slug>/<sid>.jsonl location, calling os.Chtimes to
// back-date it, and asserting the result. It cleans up after itself.
func TestIsResumeStale(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	// Use a unique cwd that nothing else would claim.
	cwd := filepath.Join(t.TempDir(), "leotest-workspace")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	sid := "test-session-" + t.Name()

	slug := ProjectSlug(cwd)
	projDir := filepath.Join(home, ".claude", "projects", slug)
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(projDir) })

	jsonlPath := filepath.Join(projDir, sid+".jsonl")
	if err := os.WriteFile(jsonlPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	// Fresh file — not stale.
	stale, age, err := IsResumeStale(cwd, sid, 12*time.Hour)
	if err != nil {
		t.Fatalf("IsResumeStale (fresh): %v", err)
	}
	if stale {
		t.Errorf("expected fresh file not stale, got stale=true age=%s", age)
	}

	// Back-date mtime to 18h ago — should be stale under 12h threshold.
	old := time.Now().Add(-18 * time.Hour)
	if err := os.Chtimes(jsonlPath, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}
	stale, age, err = IsResumeStale(cwd, sid, 12*time.Hour)
	if err != nil {
		t.Fatalf("IsResumeStale (old): %v", err)
	}
	if !stale {
		t.Errorf("expected old file stale, got stale=false age=%s", age)
	}
	if age < 17*time.Hour {
		t.Errorf("age = %s, want ~18h", age)
	}

	// Missing file — not stale (claude will create it).
	stale, age, err = IsResumeStale(cwd, "nonexistent-"+sid, 12*time.Hour)
	if err != nil {
		t.Fatalf("IsResumeStale (missing): %v", err)
	}
	if stale {
		t.Errorf("expected missing file not stale, got stale=true age=%s", age)
	}

	// Disabled (maxAge=0) — not stale.
	stale, _, err = IsResumeStale(cwd, sid, 0)
	if err != nil {
		t.Fatalf("IsResumeStale (disabled): %v", err)
	}
	if stale {
		t.Errorf("expected disabled to return not stale")
	}

	// Empty session ID — not stale.
	stale, _, err = IsResumeStale(cwd, "", 12*time.Hour)
	if err != nil {
		t.Fatalf("IsResumeStale (empty sid): %v", err)
	}
	if stale {
		t.Errorf("expected empty sid to return not stale")
	}
}

// TestLatestSession covers the newest-jsonl lookup used by supervisor and
// agent restore to avoid resuming stale sessions after a /clear.
func TestLatestSession(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	cwd := filepath.Join(t.TempDir(), "leotest-latest")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}

	slug := ProjectSlug(cwd)
	projDir := filepath.Join(home, ".claude", "projects", slug)
	t.Cleanup(func() { _ = os.RemoveAll(projDir) })

	// 1. Project dir does not exist yet → empty result, no error.
	sid, mt, err := LatestSession(cwd, 0)
	if err != nil {
		t.Fatalf("LatestSession (missing dir): %v", err)
	}
	if sid != "" || !mt.IsZero() {
		t.Errorf("expected empty result for missing dir, got sid=%q mt=%v", sid, mt)
	}

	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}

	// 2. Empty dir → empty result.
	sid, _, err = LatestSession(cwd, 0)
	if err != nil {
		t.Fatalf("LatestSession (empty dir): %v", err)
	}
	if sid != "" {
		t.Errorf("expected empty result for empty dir, got %q", sid)
	}

	// 3. Two jsonls with different mtimes — newest wins. Also drop a
	//    non-jsonl file that should be ignored.
	older := filepath.Join(projDir, "older-session.jsonl")
	newer := filepath.Join(projDir, "newer-session.jsonl")
	noise := filepath.Join(projDir, "not-a-session.txt")
	for _, p := range []string{older, newer, noise} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatalf("Chtimes older: %v", err)
	}

	sid, mt, err = LatestSession(cwd, 0)
	if err != nil {
		t.Fatalf("LatestSession (two files): %v", err)
	}
	if sid != "newer-session" {
		t.Errorf("expected newer-session, got %q", sid)
	}
	if time.Since(mt) > time.Minute {
		t.Errorf("unexpected mtime %v", mt)
	}

	// 4. Staleness threshold trips — everything older than maxAge is ignored.
	tooOld := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(older, tooOld, tooOld); err != nil {
		t.Fatalf("Chtimes older: %v", err)
	}
	if err := os.Chtimes(newer, tooOld, tooOld); err != nil {
		t.Fatalf("Chtimes newer: %v", err)
	}
	sid, _, err = LatestSession(cwd, 12*time.Hour)
	if err != nil {
		t.Fatalf("LatestSession (all stale): %v", err)
	}
	if sid != "" {
		t.Errorf("expected empty result when newest is stale, got %q", sid)
	}

	// 5. maxAge=0 disables the staleness check — same stale files still return.
	sid, _, err = LatestSession(cwd, 0)
	if err != nil {
		t.Fatalf("LatestSession (maxAge=0 stale): %v", err)
	}
	if sid == "" {
		t.Errorf("expected a result with maxAge=0 even when files are old")
	}
}
