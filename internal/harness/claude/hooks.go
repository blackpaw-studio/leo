package claude

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TurnHooks adds Claude's native lifecycle hooks to its existing settings
// document. The report command exits successfully unless dispatch launch
// variables are present, so ordinary Claude sessions remain unaffected.
func (Claude) TurnHooks(reportCmd []string) ([]string, error) {
	if len(reportCmd) == 0 {
		return nil, fmt.Errorf("claude: empty dispatch report command")
	}
	settings := map[string]any{"crossSessionInbound": "accept", "hooks": map[string]any{}}
	hooks := settings["hooks"].(map[string]any)
	command := shellCommand(reportCmd)
	for _, event := range []string{"Stop", "UserPromptSubmit", "SessionEnd"} {
		hooks[event] = []any{map[string]any{"type": "command", "command": command}}
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("claude: encoding hook settings: %w", err)
	}
	return []string{"--settings", string(encoded)}, nil
}

// PrepareInteractive is intentionally a no-op: Claude's existing supervisor
// dialog policy handles its non-consequential onboarding prompts, and it has
// no prelaunch trust-file configuration equivalent to Codex.
func (Claude) PrepareInteractive(string) error { return nil }

func shellCommand(args []string) string {
	words := make([]string, 0, len(args))
	for _, arg := range args {
		if arg != "" && !strings.ContainsAny(arg, " \t\n\r'\"\\$`;&|<>()*?[]{}!") {
			words = append(words, arg)
			continue
		}
		words = append(words, "'"+strings.ReplaceAll(arg, "'", "'\\\"'\\\"'")+"'")
	}
	return strings.Join(words, " ")
}
