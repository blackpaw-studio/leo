package codex

import (
	"fmt"
	"sort"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// harness_options keys accepted by the codex adapter.
var optionKeys = []string{"sandbox"}

var validSandboxes = map[string]bool{
	"read-only": true, "workspace-write": true, "danger-full-access": true,
}

// Options carries the codex-specific knobs. LeoMCP is runtime-only, filled
// by the task runner when leo's MCP server is wired in.
type Options struct {
	Sandbox string // "" = codex default (read-only)
	LeoMCP  *LeoMCPBridge
}

// LeoMCPBridge describes the per-invocation `-c mcp_servers.leo.*` config
// overrides that register leo's MCP server for one codex run. EnvVars is a
// parent-env whitelist (values stay out of ps-visible argv); ApprovalMode
// "approve" is required or headless exec auto-cancels every MCP tool call.
type LeoMCPBridge struct {
	Command      string
	Args         []string
	EnvVars      []string
	ApprovalMode string
}

// configArgs renders the bridge as repeated -c key=value overrides using
// TOML value syntax (strings quoted, arrays bracketed).
func (b *LeoMCPBridge) configArgs() []string {
	if b == nil {
		return nil
	}
	return []string{
		"-c", fmt.Sprintf("mcp_servers.leo.command=%s", tomlString(b.Command)),
		"-c", fmt.Sprintf("mcp_servers.leo.args=%s", tomlStringArray(b.Args)),
		"-c", fmt.Sprintf("mcp_servers.leo.env_vars=%s", tomlStringArray(b.EnvVars)),
		"-c", fmt.Sprintf("mcp_servers.leo.default_tools_approval_mode=%s", tomlString(b.ApprovalMode)),
	}
}

// tomlString renders s as a TOML basic string, escaping backslashes, quotes,
// and the control characters (newline, tab, carriage return) TOML basic
// strings forbid as literal bytes — required for multi-line values like the
// Leo system-context nudge.
func tomlString(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
		"\r", `\r`,
		"\t", `\t`,
	)
	return `"` + r.Replace(s) + `"`
}

func tomlStringArray(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, it := range items {
		quoted = append(quoted, tomlString(it))
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

// DecodeOptions strictly decodes a harness_options map into Options.
// Removed/unsupported knobs get pointed rejections rather than the generic
// unknown-key error. Keys are processed in sorted order so multi-error maps
// fail deterministically.
func (Codex) DecodeOptions(raw map[string]any) (any, error) {
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
		case "sandbox":
			var s string
			if s, err = stringOption(key, val); err == nil {
				if s != "" && !validSandboxes[s] {
					err = fmt.Errorf("sandbox %q is not valid (use read-only, workspace-write, or danger-full-access)", s)
				} else {
					o.Sandbox = s
				}
			}
		case "approval":
			err = fmt.Errorf("option %q is not supported: leo always launches codex with approval policy %q (unattended sessions)", key, "never")
		case "append_system_prompt":
			err = fmt.Errorf("option %q is not supported: codex has no append-system-prompt equivalent (use the workspace AGENTS.md)", key)
		default:
			err = fmt.Errorf("unknown option %q (valid: %s)", key, strings.Join(optionKeys, ", "))
		}
		if err != nil {
			return nil, err
		}
	}
	return o, nil
}

// OptionsSchema describes the codex harness_options for web forms. Keys
// mirror optionKeys; TestOptionsSchemaMatchesDecodeOptions locks the two.
func (Codex) OptionsSchema() []harness.OptionField {
	return []harness.OptionField{
		{Key: "sandbox", Label: "Sandbox", Type: harness.OptionEnum,
			EnumValues: []string{"read-only", "workspace-write", "danger-full-access"},
			Help:       "codex exec sandbox policy (default read-only)"},
	}
}

func stringOption(key string, val any) (string, error) {
	s, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("option %q must be a string, got %T", key, val)
	}
	return s, nil
}
