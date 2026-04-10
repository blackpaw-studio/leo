package pluginsync

import (
	"strings"
	"testing"
)

func TestEmbeddedPluginContainsAgentCommands(t *testing.T) {
	data, err := pluginFiles.ReadFile("telegram/server.ts")
	if err != nil {
		t.Fatalf("reading embedded server.ts: %v", err)
	}

	content := string(data)
	lines := strings.Count(content, "\n")
	t.Logf("embedded server.ts: %d lines", lines)

	for _, cmd := range []string{"stop", "agent", "agents", "clear", "compact", "tasks"} {
		if !strings.Contains(content, `bot.command("`+cmd+`"`) {
			t.Errorf("embedded server.ts missing /%s command", cmd)
		}
	}

	commands := strings.Count(content, "bot.command(")
	if commands != 6 {
		t.Errorf("expected 6 bot.command() calls, got %d", commands)
	}
}
