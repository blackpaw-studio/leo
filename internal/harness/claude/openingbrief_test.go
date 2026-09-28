package claude

import (
	"path/filepath"
	"testing"
)

// TestValidAgentBriefPath is the defense-in-depth check the security review
// asked for: an agentstore.Record.OpeningBriefPath round-trips through JSON
// on disk, so it must be validated as untrusted input before anything treats
// it as safe to embed unquoted in a shell command. Only an exact
// AgentBriefPath result for some name is accepted.
func TestValidAgentBriefPath(t *testing.T) {
	home := "/home/u/.leo"
	briefDir := filepath.Join(home, "state", AgentBriefSpillDir)

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"exact AgentBriefPath result", AgentBriefPath(home, "myagent"), true},
		{"empty path", "", false},
		{"path traversal escaping the dir", filepath.Join(briefDir, "..", "..", "etc", "passwd"), false},
		{"absolute path elsewhere entirely", "/etc/passwd", false},
		{"right dir, wrong extension", filepath.Join(briefDir, "myagent.json"), false},
		{"nested subdirectory under the brief dir", filepath.Join(briefDir, "sub", "myagent.txt"), false},
		{"nul-prefixed sentinel from the old design", "\x00leo-raw-argv\x00" + AgentBriefPath(home, "myagent"), false},
		{"nul-prefixed sentinel with shell metacharacters", "\x00leo-raw-argv\x00$(rm -rf /)", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidAgentBriefPath(home, tt.path); got != tt.want {
				t.Errorf("ValidAgentBriefPath(%q, %q) = %v, want %v", home, tt.path, got, tt.want)
			}
		})
	}
}

// TestValidAgentBriefPathEmptyHome confirms an empty homePath never validates
// anything — there is no directory to anchor the check against.
func TestValidAgentBriefPathEmptyHome(t *testing.T) {
	if ValidAgentBriefPath("", "/some/state/agent-briefs/x.txt") {
		t.Error("expected false with an empty homePath")
	}
}
