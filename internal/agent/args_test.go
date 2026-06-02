package agent

import (
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// hasFlagValue reports whether args contains `flag` immediately followed by a
// value that contains `substr`.
func hasFlagValue(args []string, flag, substr string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && strings.Contains(args[i+1], substr) {
			return true
		}
	}
	return false
}

func TestBuildTemplateArgsWiresLeoMCPWhenWebEnabled(t *testing.T) {
	// HomePath must be set: AppendArg writes leo-mcp.json under StatePath
	// (HomePath/state). An empty HomePath would write relative to cwd.
	cfg := &config.Config{HomePath: t.TempDir(), Web: config.WebConfig{Enabled: true}}
	tmpl := config.TemplateConfig{}

	args := BuildTemplateArgs(cfg, tmpl, "agent-x", "/tmp/ws", "")

	if !hasFlagValue(args, "--mcp-config", "leo-mcp.json") {
		t.Errorf("expected --mcp-config pointing at leo-mcp.json; got %v", args)
	}
	if !hasFlagValue(args, "--append-system-prompt", "leo_send_message") {
		t.Errorf("expected awareness line in --append-system-prompt; got %v", args)
	}
}

func TestBuildTemplateArgsNoLeoMCPWhenWebDisabled(t *testing.T) {
	cfg := &config.Config{HomePath: t.TempDir(), Web: config.WebConfig{Enabled: false}}
	args := BuildTemplateArgs(cfg, config.TemplateConfig{}, "agent-x", "/tmp/ws", "")

	if hasFlagValue(args, "--append-system-prompt", "leo_send_message") {
		t.Errorf("awareness line must not appear when web disabled; got %v", args)
	}
}
