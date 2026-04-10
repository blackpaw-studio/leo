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

	if !strings.Contains(content, `bot.command("agent"`) {
		t.Error("embedded server.ts missing /agent command")
	}
	if !strings.Contains(content, `bot.command("agents"`) {
		t.Error("embedded server.ts missing /agents command")
	}
	if !strings.Contains(content, "pendingTemplateSelection") {
		t.Error("embedded server.ts missing pendingTemplateSelection")
	}

	commands := strings.Count(content, "bot.command(")
	if commands != 3 {
		t.Errorf("expected 3 bot.command() calls, got %d", commands)
	}
}
