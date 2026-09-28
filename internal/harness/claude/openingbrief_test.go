package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidAgentBriefID locks the exact shape an opening-prompt brief id may
// take: 32 lowercase hex characters, nothing else. This is the gate
// AgentBriefPathForID enforces before an id becomes a path — an id that
// fails this check must never reach a path expression at all.
func TestValidAgentBriefID(t *testing.T) {
	id, err := GenerateAgentBriefID()
	if err != nil {
		t.Fatalf("GenerateAgentBriefID: %v", err)
	}

	tests := []struct {
		name string
		id   string
		want bool
	}{
		{"freshly generated id", id, true},
		{"empty", "", false},
		{"too short", "abc123", false},
		{"uppercase hex rejected", strings.ToUpper(id), false},
		{"path traversal", "../../etc/passwd", false},
		{"old nul-sentinel bytes", "\x00leo-raw-argv\x00", false},
		{"shell metacharacters", "$(rm -rf /)", false},
		{"32 hex chars exactly", "0123456789abcdef0123456789abcdef", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidAgentBriefID(tt.id); got != tt.want {
				t.Errorf("ValidAgentBriefID(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

// TestAgentBriefPathForID confirms the derived path is always exactly
// <home>/state/agent-briefs/<id>.txt for a valid id, and that an invalid id
// never reaches path construction at all — it errors instead.
func TestAgentBriefPathForID(t *testing.T) {
	home := "/home/u/.leo"
	id := "0123456789abcdef0123456789abcdef"

	path, err := AgentBriefPathForID(home, id)
	if err != nil {
		t.Fatalf("AgentBriefPathForID: %v", err)
	}
	want := filepath.Join(home, "state", AgentBriefSpillDir, id+".txt")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}

	invalidIDs := []string{"", "../../etc/passwd", "not-hex-at-all", "\x00leo-raw-argv\x00" + id}
	for _, bad := range invalidIDs {
		if _, err := AgentBriefPathForID(home, bad); err == nil {
			t.Errorf("AgentBriefPathForID(%q) succeeded, want an error", bad)
		}
	}

	if _, err := AgentBriefPathForID("", id); err == nil {
		t.Error("AgentBriefPathForID with empty homePath succeeded, want an error")
	}
}

// TestVerifyAgentBriefFile confirms the Lstat-based safety check: a proper
// WritePrivateBrief-created file passes, and a symlink at the exact same
// path — which a shell $(cat ...) would otherwise happily follow — is
// refused. Lstat (not Stat) is what makes this possible: it reports the
// symlink itself rather than resolving through it.
func TestVerifyAgentBriefFile(t *testing.T) {
	dir := t.TempDir()

	good := filepath.Join(dir, "good.txt")
	if err := WritePrivateBrief(good, "hello"); err != nil {
		t.Fatalf("WritePrivateBrief: %v", err)
	}
	if err := VerifyAgentBriefFile(good); err != nil {
		t.Errorf("VerifyAgentBriefFile on a normal brief file: %v", err)
	}

	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("/etc/passwd contents pretend"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(dir, "symlink.txt")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAgentBriefFile(symlink); err == nil {
		t.Error("VerifyAgentBriefFile accepted a symlink, want a refusal")
	}

	wrongMode := filepath.Join(dir, "wrongmode.txt")
	if err := os.WriteFile(wrongMode, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAgentBriefFile(wrongMode); err == nil {
		t.Error("VerifyAgentBriefFile accepted mode 0644, want a refusal")
	}

	if err := VerifyAgentBriefFile(filepath.Join(dir, "missing.txt")); err == nil {
		t.Error("VerifyAgentBriefFile accepted a missing file, want an error")
	}

	dirAsPath := filepath.Join(dir, "adir")
	if err := os.Mkdir(dirAsPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAgentBriefFile(dirAsPath); err == nil {
		t.Error("VerifyAgentBriefFile accepted a directory, want a refusal")
	}
}

// TestGenerateAgentBriefIDUnique confirms two calls never collide in any
// reasonable test run — GenerateAgentBriefID exists specifically to avoid the
// name-derived collision a rename could otherwise cause.
func TestGenerateAgentBriefIDUnique(t *testing.T) {
	a, err := GenerateAgentBriefID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateAgentBriefID()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("two generated ids collided: %q", a)
	}
	if !ValidAgentBriefID(a) || !ValidAgentBriefID(b) {
		t.Fatalf("generated ids must be valid: %q, %q", a, b)
	}
}
