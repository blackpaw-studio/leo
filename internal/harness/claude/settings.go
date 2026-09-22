package claude

import (
	"encoding/json"
	"fmt"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// attentionNotificationMatcher limits the Notification hook to the prompts
// that block on the operator.
const attentionNotificationMatcher = "permission_prompt|elicitation_dialog"

// AttentionHooks is TurnHooks plus a Notification group, so a supervised
// agent also reports when it is blocked on a permission prompt or an
// elicitation dialog.
func (c Claude) AttentionHooks(reportCmd []string) ([]string, error) {
	turn, err := c.TurnHooks(reportCmd)
	if err != nil {
		return nil, err
	}
	notification, err := json.Marshal(map[string]any{"hooks": map[string]any{
		"Notification": []any{map[string]any{
			"matcher": attentionNotificationMatcher,
			"hooks":   []any{map[string]any{"type": "command", "command": shellCommand(reportCmd)}},
		}},
	}})
	if err != nil {
		return nil, fmt.Errorf("claude: encoding notification hook: %w", err)
	}
	return MergeSettingsArgs(turn, []string{"--settings", string(notification)})
}

// attentionLaunch backs the driver's harness.AttentionHooker capability.
func attentionLaunch(_ harness.SessionHandle, args, reportCmd []string) ([]string, error) {
	hooks, err := (Claude{}).AttentionHooks(reportCmd)
	if err != nil {
		return nil, err
	}
	return MergeSettingsArgs(args, hooks)
}

// MergeSettingsArgs folds every `--settings <json>` in args and extra into a
// single trailing --settings, because claude honors only one. Top-level keys
// merge, and object values (e.g. hooks) merge one level deep, later wins;
// other flags keep their order. Neither input is mutated.
func MergeSettingsArgs(args, extra []string) ([]string, error) {
	settings := map[string]any{}
	merge := func(raw string) error {
		var next map[string]any
		if err := json.Unmarshal([]byte(raw), &next); err != nil {
			return err
		}
		for key, value := range next {
			if child, ok := value.(map[string]any); ok {
				if existing, ok := settings[key].(map[string]any); ok {
					for childKey, childValue := range child {
						existing[childKey] = childValue
					}
					continue
				}
			}
			settings[key] = value
		}
		return nil
	}
	base := make([]string, 0, len(args)+len(extra))
	for _, group := range []struct {
		name string
		argv []string
	}{{"base", args}, {"extra", extra}} {
		for i := 0; i < len(group.argv); i++ {
			if group.argv[i] == "--settings" && i+1 < len(group.argv) {
				if err := merge(group.argv[i+1]); err != nil {
					return nil, fmt.Errorf("merge %s settings: %w", group.name, err)
				}
				i++
				continue
			}
			base = append(base, group.argv[i])
		}
	}
	if len(settings) == 0 {
		return base, nil
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("encode merged settings: %w", err)
	}
	return append(base, "--settings", string(encoded)), nil
}
