package observe

import (
	"encoding/json"
	"net/url"
	"strings"
)

// MaxToolFieldChars caps each field ToolActivity returns, as the claude mod's
// MAX_SUMMARY_CHARS did; the attention store clamps again.
const MaxToolFieldChars = 200

// ToolActivity says what an attention report may show of a tool call: its
// name and a one-field summary of its input, never the input itself (a Bash
// command's first word, a file path with home spelled `~`, a search pattern,
// a fetched URL's host). It ports the mod's toolActivity so a hook-sourced
// reason reads like the bridge-sourced one did. An unknown tool or malformed
// input has an empty summary.
func ToolActivity(tool string, input json.RawMessage, home string) (name, summary string) {
	return capToolField(tool), capToolField(toolSummary(tool, input, home))
}

func toolSummary(tool string, input json.RawMessage, home string) string {
	var args map[string]any
	if json.Unmarshal(input, &args) != nil {
		return ""
	}
	str := func(key string) string {
		s, _ := args[key].(string)
		return s
	}
	switch tool {
	case "Bash":
		if fields := strings.Fields(str("command")); len(fields) > 0 {
			return fields[0]
		}
	case "Read", "Edit", "Write":
		return abbreviateHome(str("file_path"), home)
	case "Grep", "Glob":
		return str("pattern")
	case "WebFetch":
		if u, err := url.Parse(str("url")); err == nil {
			return u.Hostname()
		}
	}
	return ""
}

func capToolField(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	if r := []rune(s); len(r) > MaxToolFieldChars {
		return string(r[:MaxToolFieldChars])
	}
	return s
}

// abbreviateHome spells path's leading home directory `~`.
func abbreviateHome(path, home string) string {
	base := strings.TrimSuffix(home, "/")
	switch {
	case base == "":
		return path
	case path == base:
		return "~"
	case strings.HasPrefix(path, base+"/"):
		return "~" + path[len(base):]
	}
	return path
}

// PermissionInput is the most useful argument of the call a permission
// prompt asks about, for a client that wants more than ToolActivity's
// one-word summary: a Bash command in full, a file tool's path (home
// spelled `~`), a search's pattern and where it looks, a fetched URL without
// credentials, query or fragment. It never returns what a tool would write:
// Write and Edit give their path only. An unknown tool or malformed input
// is empty. The caller clamps it (ClampAttentionReason).
func PermissionInput(tool string, input json.RawMessage, home string) string {
	var args map[string]any
	if json.Unmarshal(input, &args) != nil {
		return ""
	}
	str := func(key string) string {
		s, _ := args[key].(string)
		return strings.TrimSpace(s)
	}
	switch tool {
	case "Bash":
		return str("command")
	case "Read", "Edit", "Write", "MultiEdit":
		return abbreviateHome(str("file_path"), home)
	case "NotebookEdit":
		return abbreviateHome(str("notebook_path"), home)
	case "Grep", "Glob":
		pattern := str("pattern")
		if path := str("path"); pattern != "" && path != "" {
			return pattern + " in " + abbreviateHome(path, home)
		}
		return pattern
	case "WebFetch":
		u, err := url.Parse(str("url"))
		if err != nil || u.Host == "" {
			return ""
		}
		u.User, u.RawQuery, u.Fragment = nil, "", ""
		return u.String()
	}
	return ""
}
