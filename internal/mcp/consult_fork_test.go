package mcp

import (
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

// A fork consult is answered in the caller's own claude session by the
// leo-bridge mod; one that reaches the server had no mod to answer it.
func TestLeoConsultForkNeedsABridgedSession(t *testing.T) {
	daemon := newOKDaemon(t)
	reg := newRegistry(newDaemonClient(daemon.port(), ""), "assistant", leotools.Permissions{})
	_, err := callTool(reg, "leo_consult", map[string]any{"prompt": "is my plan sane?", "fork": true})
	if err == nil || err.Error() != "fork consult needs a bridged claude session" {
		t.Fatalf("fork consult err = %v, want the bridged-session error", err)
	}
	if got := len(daemon.recorded()); got != 0 {
		t.Fatalf("a fork consult reached the daemon (%d calls)", got)
	}
}

// fork ignores template and model, so only the prompt is required, and
// the description says when a fork is the right call.
func TestLeoConsultSchemaOffersFork(t *testing.T) {
	reg := newRegistry(newDaemonClient("0", ""), "assistant", leotools.Permissions{})
	for _, d := range reg.list() {
		if d.Name != "leo_consult" {
			continue
		}
		props := d.InputSchema["properties"].(map[string]any)
		fork, ok := props["fork"].(map[string]any)
		if !ok || fork["type"] != "boolean" {
			t.Fatalf("fork property = %v, want a boolean", props["fork"])
		}
		desc := strings.ToLower(fork["description"].(string))
		for _, want := range []string{"same model", "sees this whole conversation", "no tools", "cheapest", "sanity check of your own plan"} {
			if !strings.Contains(desc, want) {
				t.Errorf("fork description missing %q: %q", want, desc)
			}
		}
		if required, _ := d.InputSchema["required"].([]string); len(required) != 1 || required[0] != "prompt" {
			t.Fatalf("required = %v, want [prompt]", d.InputSchema["required"])
		}
		return
	}
	t.Fatal("leo_consult not registered")
}

// Without fork a template is still required.
func TestLeoConsultWithoutForkNeedsATemplate(t *testing.T) {
	daemon := newOKDaemon(t)
	reg := newRegistry(newDaemonClient(daemon.port(), ""), "assistant", leotools.Permissions{})
	if _, err := callTool(reg, "leo_consult", map[string]any{"prompt": "hi"}); err == nil || !strings.Contains(err.Error(), "template") {
		t.Fatalf("consult without template err = %v, want a template error", err)
	}
	if got := len(daemon.recorded()); got != 0 {
		t.Fatalf("an invalid consult reached the daemon (%d calls)", got)
	}
}
