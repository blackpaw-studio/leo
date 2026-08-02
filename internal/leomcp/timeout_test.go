package leomcp

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/consult"
)

// TestToolTimeoutExceedsConsultDeadline locks the ordering that makes leo's
// own timeout authoritative: a harness must not kill a leo MCP tool call
// before the daemon has had the chance to return its structured error.
func TestToolTimeoutExceedsConsultDeadline(t *testing.T) {
	if ToolTimeout <= consult.RunTimeout {
		t.Fatalf("ToolTimeout (%s) must exceed consult.RunTimeout (%s)", ToolTimeout, consult.RunTimeout)
	}
}
