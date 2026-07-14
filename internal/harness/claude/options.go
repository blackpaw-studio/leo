package claude

import (
	"fmt"
	"sort"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// harness_options keys accepted by the claude adapter. These mirror the
// pre-break flat config field names one-to-one.
var optionKeys = []string{
	"agent",
	"allowed_tools",
	"append_system_prompt",
	"bypass_permissions",
	"disallowed_tools",
	"permission_mode",
	"remote_control",
}

var validPermissionModes = map[string]bool{
	"acceptEdits": true, "auto": true, "bypassPermissions": true,
	"default": true, "dontAsk": true, "plan": true,
}

// DecodeOptions strictly decodes a harness_options map into Options.
// Runtime fields (MCPConfigPath, LeoMCPArgs) stay zero.
// Keys are processed in sorted order so multi-error maps fail deterministically.
func (Claude) DecodeOptions(raw map[string]any) (any, error) {
	var o Options
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		val := raw[key]
		var err error
		switch key {
		case "permission_mode":
			o.PermissionMode, err = stringOption(key, val)
			if err == nil && o.PermissionMode != "" && !validPermissionModes[o.PermissionMode] {
				err = fmt.Errorf("permission_mode %q is not valid (use acceptEdits, auto, bypassPermissions, default, dontAsk, or plan)", o.PermissionMode)
			}
		case "bypass_permissions":
			o.BypassPermissions, err = boolOption(key, val)
		case "remote_control":
			o.RemoteControl, err = boolOption(key, val)
		case "agent":
			o.AgentFile, err = stringOption(key, val)
		case "allowed_tools":
			o.AllowedTools, err = stringSliceOption(key, val)
		case "disallowed_tools":
			o.DisallowedTools, err = stringSliceOption(key, val)
		case "append_system_prompt":
			o.AppendSystemPrompt, err = stringOption(key, val)
		default:
			err = fmt.Errorf("unknown option %q (valid: %s)", key, strings.Join(optionKeys, ", "))
		}
		if err != nil {
			return nil, err
		}
	}
	return o, nil
}

// OptionsSchema describes the claude harness_options for web forms. Keys
// mirror optionKeys; TestOptionsSchemaMatchesDecodeOptions locks the two.
func (Claude) OptionsSchema() []harness.OptionField {
	return []harness.OptionField{
		{Key: "permission_mode", Label: "Permission mode", Type: harness.OptionEnum,
			EnumValues: []string{"acceptEdits", "auto", "bypassPermissions", "default", "dontAsk", "plan"},
			Help:       "--permission-mode for the spawned claude"},
		{Key: "bypass_permissions", Label: "Bypass permissions", Type: harness.OptionBool,
			Help: "--dangerously-skip-permissions"},
		{Key: "remote_control", Label: "Remote control", Type: harness.OptionBool,
			Help: "--remote-control (claude.ai remote control)"},
		{Key: "agent", Label: "Agent", Type: harness.OptionString, Source: "agents",
			Help: "--agent: named claude sub-agent"},
		{Key: "allowed_tools", Label: "Allowed tools", Type: harness.OptionStringList,
			Help: "--allowed-tools, comma-separated"},
		{Key: "disallowed_tools", Label: "Disallowed tools", Type: harness.OptionStringList,
			Help: "--disallowed-tools, comma-separated"},
		{Key: "append_system_prompt", Label: "Append system prompt", Type: harness.OptionText,
			Help: "--append-system-prompt"},
	}
}

func stringOption(key string, val any) (string, error) {
	s, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("option %q must be a string, got %T", key, val)
	}
	return s, nil
}

func boolOption(key string, val any) (bool, error) {
	b, ok := val.(bool)
	if !ok {
		return false, fmt.Errorf("option %q must be a boolean, got %T", key, val)
	}
	return b, nil
}

func stringSliceOption(key string, val any) ([]string, error) {
	items, ok := val.([]any)
	if !ok {
		return nil, fmt.Errorf("option %q must be a list of strings, got %T", key, val)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("option %q must be a list of strings, got %T element", key, item)
		}
		out = append(out, s)
	}
	return out, nil
}
