package mcp

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

// TestHarness drives a real tool registry from an external test package:
// the registry and its options are unexported, and the cross-host
// integration test needs the real MCP code path against real web servers.
type TestHarness struct{ reg *registry }

// NewTestHarness builds the registry a supervised agent named process would
// get, bound to the daemon on port. A non-nil peers transport replaces the
// one used to reach peer hosts, so the test can capture the wire.
func NewTestHarness(port, token, process string, perms leotools.Permissions, peers http.RoundTripper) *TestHarness {
	rc := newRemoteClient()
	if peers != nil {
		rc.http.Transport = peers
	}
	return &TestHarness{reg: newRegistry(newDaemonClient(port, token), process, perms, withRemote(rc))}
}

// Call runs one tool and returns its text result or error.
func (h *TestHarness) Call(tool string, args map[string]any) (string, error) {
	return callToolForTest(h.reg, tool, args)
}

func callToolForTest(r *registry, tool string, args map[string]any) (string, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	return r.callContext(context.Background(), tool, raw)
}
