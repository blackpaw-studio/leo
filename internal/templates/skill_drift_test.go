package templates

import (
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/channels"
)

// TestClaudeWorkspaceSkillMentionsEveryCanonicalCommand asserts the supervised
// Claude's skill text references every canonical channel command name. This
// guards against the skill drifting away from the channels.Canonical list and
// the leo MCP server tool surface.
//
// If you intentionally remove a canonical command, drop it from
// channels.Canonical *and* the claude-workspace.md skill in the same change.
func TestClaudeWorkspaceSkillMentionsEveryCanonicalCommand(t *testing.T) {
	rendered, err := RenderClaudeWorkspace(AgentData{Workspace: "/tmp/test"})
	if err != nil {
		t.Fatalf("render skill: %v", err)
	}
	for _, cmd := range channels.Canonical {
		token := "/" + cmd.Name
		if !strings.Contains(rendered, token) {
			t.Errorf("skill missing canonical command %q", token)
		}
	}
}
