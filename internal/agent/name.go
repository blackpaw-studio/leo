package agent

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	agentNamePrefix    = "leo-"
	maxAgentNameLength = 64
)

// charsetRe rejects anything outside lowercase alphanumerics and dashes. It is
// applied to the body after the leo- prefix is stripped, so the user's input
// must be tmux-safe and slug-shaped (no dots, colons, slashes, spaces, etc.).
var charsetRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// NormalizeAgentName validates and canonicalizes a user-supplied agent name.
// It lowercases, ensures exactly one leo- prefix, rejects tmux-hostile
// characters, collapses repeated/edge dashes, and caps the total length so the
// stored name always equals the tmux session name (agent.SessionName).
func NormalizeAgentName(raw string) (string, error) {
	body := strings.ToLower(strings.TrimSpace(raw))
	body = strings.TrimPrefix(body, agentNamePrefix)
	if body == "" {
		return "", fmt.Errorf("agent name is empty")
	}
	if !charsetRe.MatchString(body) {
		return "", fmt.Errorf("agent name %q has invalid characters (allowed: a-z, 0-9, dash)", raw)
	}
	// Collapse runs of dashes and trim leading/trailing dashes.
	for strings.Contains(body, "--") {
		body = strings.ReplaceAll(body, "--", "-")
	}
	body = strings.Trim(body, "-")
	if body == "" {
		return "", fmt.Errorf("agent name %q reduces to empty after normalization", raw)
	}
	name := agentNamePrefix + body
	if len(name) > maxAgentNameLength {
		name = name[:maxAgentNameLength]
		name = strings.TrimRight(name, "-")
	}
	return name, nil
}
