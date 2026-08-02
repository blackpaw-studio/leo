package opencode

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// harness_options keys accepted by the opencode adapter.
var optionKeys = []string{"permission"}

var validPermissionValues = map[string]bool{"allow": true, "ask": true, "deny": true}

// Options carries the opencode-specific knobs. LeoMCP is runtime-only:
// filled by the task runner or spawn builder when leo's MCP server is wired
// in — never set from harness_options.
type Options struct {
	Permission map[string]any // tool → "allow"|"ask"|"deny", or pattern map of the same
	LeoMCP     *LeoMCPBridge
}

// LeoMCPBridge describes the leo MCP server entry injected into the
// per-spawn OPENCODE_CONFIG_CONTENT overlay (deep-merged over the user's
// own opencode config; no file mutation). ToolTimeout overrides opencode's
// own per-server MCP deadline, which would otherwise truncate long-running
// leo tools like leo_consult; zero leaves opencode's default in place.
type LeoMCPBridge struct {
	Command     []string
	Env         map[string]string
	ToolTimeout time.Duration
}

// DecodeOptions strictly decodes a harness_options map into Options. Keys
// are processed in sorted order so multi-error maps fail deterministically.
func (Opencode) DecodeOptions(raw map[string]any) (any, error) {
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
		case "permission":
			o.Permission, err = permissionOption(val)
		case "append_system_prompt":
			err = fmt.Errorf("option %q is not supported: opencode has no append-system-prompt equivalent (use AGENTS.md or the instructions config)", key)
		default:
			err = fmt.Errorf("unknown option %q (valid: %s)", key, strings.Join(optionKeys, ", "))
		}
		if err != nil {
			return nil, err
		}
	}
	return o, nil
}

// OptionsSchema describes the opencode harness_options for web forms. Keys
// mirror optionKeys; TestOptionsSchemaMatchesDecodeOptions locks the two.
func (Opencode) OptionsSchema() []harness.OptionField {
	return []harness.OptionField{
		{Key: "permission", Label: "Permission", Type: harness.OptionYAMLMap,
			Help: "YAML map: tool → allow/ask/deny, or tool → {pattern: verdict}"},
	}
}

func permissionOption(val any) (map[string]any, error) {
	m, ok := val.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("option %q must be a map, got %T", "permission", val)
	}
	out := make(map[string]any, len(m))
	for tool, v := range m {
		switch tv := v.(type) {
		case string:
			if !validPermissionValues[tv] {
				return nil, fmt.Errorf("permission value %q for %q is not valid (use allow, ask, or deny)", tv, tool)
			}
			out[tool] = tv
		case map[string]any:
			patterns := make(map[string]any, len(tv))
			for pat, pv := range tv {
				s, ok := pv.(string)
				if !ok || !validPermissionValues[s] {
					return nil, fmt.Errorf("permission value %q for %q is not valid (use allow, ask, or deny)", fmt.Sprint(pv), pat)
				}
				patterns[pat] = s
			}
			out[tool] = patterns
		default:
			return nil, fmt.Errorf("option %q values must be %q/%q/%q or a pattern map, got %T for %q", "permission", "allow", "ask", "deny", v, tool)
		}
	}
	return out, nil
}

// Env builds the launch env overlay: OPENCODE_CONFIG_CONTENT (the leo MCP
// server entry when wired, plus the user's permission map). Returns nil when
// there is nothing to inject.
func (Opencode) Env(spec harness.LaunchSpec) (map[string]string, error) {
	opts, ok := spec.Options.(Options)
	if !ok {
		return nil, fmt.Errorf("opencode: spec.Options is %T, want opencode.Options", spec.Options)
	}
	env := map[string]string{}
	if opts.LeoMCP != nil || len(opts.Permission) > 0 {
		cfg := map[string]any{}
		if opts.LeoMCP != nil {
			leo := map[string]any{
				"type":        "local",
				"command":     opts.LeoMCP.Command,
				"enabled":     true,
				"environment": opts.LeoMCP.Env,
			}
			if opts.LeoMCP.ToolTimeout > 0 {
				// opencode's per-server timeout is milliseconds.
				leo["timeout"] = opts.LeoMCP.ToolTimeout.Milliseconds()
			}
			cfg["mcp"] = map[string]any{"leo": leo}
		}
		if len(opts.Permission) > 0 {
			cfg["permission"] = opts.Permission
		}
		content, err := json.Marshal(cfg)
		if err != nil {
			return nil, fmt.Errorf("opencode: marshaling config content: %w", err)
		}
		env["OPENCODE_CONFIG_CONTENT"] = string(content)
	}
	if len(env) == 0 {
		return nil, nil
	}
	return env, nil
}
