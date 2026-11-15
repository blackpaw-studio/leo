package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// leoMCPServerName is the bridge's key in a strict MCP config; account
// servers may not shadow it.
const leoMCPServerName = "leo"

// emptyStrictMCPConfig is the strict config of a run with no MCP servers.
const emptyStrictMCPConfig = `{"mcpServers":{}}`

// MCPServerDefs maps a server name to its definition, kept verbatim: a
// definition can carry secrets in url, headers or env, so it is never parsed
// into a struct that could be printed.
type MCPServerDefs map[string]json.RawMessage

// ResolveMCPServers looks names up in the top-level mcpServers of the
// account state file the launch env selects ($CLAUDE_CONFIG_DIR/.claude.json,
// else $HOME/.claude.json). Unknown names, a missing or malformed file and the
// reserved name "leo" become warnings, never errors. Warnings carry names and
// the file path only, never definitions.
func ResolveMCPServers(env map[string]string, names []string) (MCPServerDefs, []string) {
	path, err := StateFile(env)
	if err != nil {
		return nil, []string{fmt.Sprintf("mcp_servers: cannot locate the account config (%v); skipped %s", err, quoteNames(names))}
	}
	return resolveMCPServers(path, names, os.ReadFile)
}

func resolveMCPServers(path string, names []string, readFile func(string) ([]byte, error)) (MCPServerDefs, []string) {
	skipAll := func(reason string) (MCPServerDefs, []string) {
		return nil, []string{fmt.Sprintf("mcp_servers: %s %s; skipped %s", path, reason, quoteNames(names))}
	}
	data, err := readFile(path)
	if err != nil {
		// The error text may embed a path; the reason alone is enough.
		if errors.Is(err, os.ErrNotExist) {
			return skipAll("does not exist")
		}
		return skipAll("is unreadable")
	}
	var doc struct {
		MCPServers json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return skipAll("is not valid JSON")
	}
	var servers map[string]json.RawMessage
	if len(doc.MCPServers) == 0 || json.Unmarshal(doc.MCPServers, &servers) != nil || servers == nil {
		return skipAll("has no mcpServers object")
	}
	defs := make(MCPServerDefs, len(names))
	var warnings []string
	for _, name := range names {
		def, ok := servers[name]
		switch {
		case name == leoMCPServerName:
			warnings = append(warnings, fmt.Sprintf("mcp_servers: %q is reserved for the leo bridge; skipped", name))
		case !ok:
			warnings = append(warnings, fmt.Sprintf("mcp_servers: %q is not defined in %s; skipped", name, path))
		case !isJSONObject(def):
			warnings = append(warnings, fmt.Sprintf("mcp_servers: %q in %s is not an object; skipped", name, path))
		default:
			defs[name] = def
		}
	}
	return defs, warnings
}

func isJSONObject(raw json.RawMessage) bool {
	var obj map[string]json.RawMessage
	return json.Unmarshal(raw, &obj) == nil && obj != nil
}

func quoteNames(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(quoted, ", ")
}

// ApplyMCPServers resolves o.MCPServers against the launch env's account
// config and folds the found servers into the strict config. It returns a
// copy and never mutates o.
//
//   - MCP "none": StrictMCPConfig becomes the base servers (the bridge, when
//     present) plus the named ones, the bridge winning a name clash, and
//     StrictMCPConfigPrivate is set when any server was added.
//   - MCP "inherit" with names: unchanged, plus one warning (inherit already
//     loads every server).
//   - MCP unset, or no names: unchanged and silent.
func ApplyMCPServers(o Options, env map[string]string) (Options, []string) {
	if len(o.MCPServers) == 0 {
		return o, nil
	}
	switch o.MCP {
	case "inherit":
		return o, []string{"mcp_servers ignored under mcp: inherit (inherit already loads every user-scope server)"}
	case "none":
	default:
		return o, nil
	}
	defs, warnings := ResolveMCPServers(env, o.MCPServers)
	if len(defs) == 0 {
		return o, warnings
	}
	merged, err := mergeStrictMCPConfig(o.StrictMCPConfig, defs)
	if err != nil {
		return o, append(warnings, "mcp_servers: the base MCP config is malformed; named servers skipped")
	}
	o.StrictMCPConfig = merged
	o.StrictMCPConfigPrivate = true
	return o, warnings
}

// mergeStrictMCPConfig adds defs to the strict config's servers; servers
// already in base win a name clash.
func mergeStrictMCPConfig(base string, defs MCPServerDefs) (string, error) {
	if base == "" {
		base = emptyStrictMCPConfig
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(base), &doc); err != nil {
		return "", err
	}
	servers := map[string]json.RawMessage{}
	if raw, ok := doc["mcpServers"]; ok {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return "", err
		}
	}
	for name, def := range defs {
		if _, taken := servers[name]; !taken {
			servers[name] = def
		}
	}
	rawServers, err := json.Marshal(servers)
	if err != nil {
		return "", err
	}
	doc["mcpServers"] = rawServers
	out, err := json.Marshal(doc)
	return string(out), err
}

// MCPConfigSpillPath is where a run's private strict MCP config is written:
// <home>/state/mcp/<name>.json. "" when name is not a plain file name.
func MCPConfigSpillPath(homePath, name string) string {
	if homePath == "" || name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return ""
	}
	return filepath.Join(homePath, "state", "mcp", name+".json")
}

// WriteMCPConfigFile writes data to path with mode 0600 in a 0700 directory,
// atomically. It refuses an empty path rather than falling back to argv.
func WriteMCPConfigFile(path string, data []byte) error {
	if path == "" {
		return errors.New("a private MCP config was built but no private path is available; refusing to inline it into argv")
	}
	return writePrivateFile(path, data)
}

// MaterializeStrictMCPConfig writes a private strict config to path and
// points StrictMCPConfigFile at it. A config that is not private is
// returned unchanged and nothing is written.
func MaterializeStrictMCPConfig(o Options, path string) (Options, error) {
	if !o.StrictMCPConfigPrivate {
		return o, nil
	}
	if err := WriteMCPConfigFile(path, []byte(o.StrictMCPConfig)); err != nil {
		return o, err
	}
	o.StrictMCPConfigFile = path
	return o, nil
}
