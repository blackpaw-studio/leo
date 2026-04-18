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
		{"absolute path with dotfile", "/Users/evan/.leo/workspace", "-Users-evan--leo-workspace"},
		{"absolute path no dots", "/Users/evan/Developer/everything-claude-code", "-Users-evan-Developer-everything-claude-code"},
		{"dotted repo", "/Users/evan/.leo/agents/leo", "-Users-evan--leo-agents-leo"},
		{"root", "/", "-"},
		{"private tmp", "/private/tmp", "-private-tmp"},
		{"empty", "", ""},
		{"trailing slash", "/Users/evan/", "-Users-evan-"},
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
	got, err := JSONLPath("/Users/evan/.leo/workspace", "abc-123")
	if err != nil {
		t.Fatalf("JSONLPath: %v", err)
	}
	want := filepath.Join(home, ".claude", "projects", "-Users-evan--leo-workspace", "abc-123.jsonl")
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
