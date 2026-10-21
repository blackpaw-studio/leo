package web

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
)

func TestClassifyEnvironmentErrorClaudeNotFound(t *testing.T) {
	status, code := ClassifyEnvironmentError(fmt.Errorf("spawning agent: %w", agent.ErrClaudeNotFound))
	if status != http.StatusFailedDependency || code != "claude_not_found" {
		t.Errorf("got %d %q, want 424 claude_not_found", status, code)
	}
}
