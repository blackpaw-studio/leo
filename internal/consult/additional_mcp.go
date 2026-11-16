package consult

import (
	"fmt"
	"regexp"
)

const (
	// MaxAdditionalMCPServers caps a dispatch's additional_mcp_servers after
	// duplicates are removed.
	MaxAdditionalMCPServers = 16
	maxMCPServerNameLen     = 64
)

var mcpServerNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// NormalizeAdditionalMCPServers validates a caller's additional_mcp_servers
// and removes duplicates in first-seen order. Empty input yields nil.
func NormalizeAdditionalMCPServers(names []string) ([]string, error) {
	var out []string
	seen := make(map[string]struct{}, len(names))
	for _, n := range names {
		if !mcpServerNamePattern.MatchString(n) {
			return nil, fmt.Errorf("invalid MCP server name %q: must be 1-%d characters of [A-Za-z0-9_.-]", n, maxMCPServerNameLen)
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	if len(out) > MaxAdditionalMCPServers {
		return nil, fmt.Errorf("too many additional MCP servers: %d (max %d)", len(out), MaxAdditionalMCPServers)
	}
	return out, nil
}
