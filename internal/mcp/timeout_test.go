package mcp

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/leomcp"
)

// TestConsultTimeoutOrdering locks the three deadlines that bracket a
// leo_consult call. The daemon's own run deadline must expire first so the
// caller gets leo's structured timeout error; the MCP client's HTTP deadline
// sits above it as a backstop; the harness-side ceiling we hand each coding
// agent sits above both, so no harness ever truncates the call itself.
func TestConsultTimeoutOrdering(t *testing.T) {
	if consultHTTPTimeout <= consult.RunTimeout {
		t.Errorf("consultHTTPTimeout (%s) must exceed consult.RunTimeout (%s)", consultHTTPTimeout, consult.RunTimeout)
	}
	if leomcp.ToolTimeout <= consultHTTPTimeout {
		t.Errorf("leomcp.ToolTimeout (%s) must exceed consultHTTPTimeout (%s)", leomcp.ToolTimeout, consultHTTPTimeout)
	}
}
