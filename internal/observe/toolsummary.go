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
