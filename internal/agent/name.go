package agent

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/blackpaw-studio/leo/internal/observe"
)

// ErrInvalidAgentName is returned (wrapped) by NormalizeAgentName for
// malformed input. Callers may map it to HTTP 400.
var ErrInvalidAgentName = errors.New("invalid agent name")

const (
	agentNamePrefix    = "leo-"
	maxAgentNameLength = 64
)

// charsetRe rejects anything outside lowercase alphanumerics and dashes. It is
// applied to the body after the leo- prefix is stripped, so the user's input
// must be tmux-safe and slug-shaped (no dots, colons, slashes, spaces, etc.).
var charsetRe = regexp.MustCompile(`^[a-z0-9-]+$`)

// DisplayName strips the canonical leo- prefix for user-facing display. It is
// the inverse of SessionName: the tmux session is always leo-<body>, while a
// stored name may or may not carry the prefix (spawn --name and rename keep the
// name as given). The prefix is a tmux implementation detail the UI shouldn't
// force users to see or retype. A name without the prefix is returned as-is.
func DisplayName(name string) string {
	return observe.DisplayName(name)
}

// NormalizeAgentName validates and canonicalizes a user-supplied agent name.
// It lowercases, ensures exactly one leo- prefix, rejects tmux-hostile
// characters, collapses repeated/edge dashes, and caps the total length so the
// prefixed form fits a tmux session name (agent.SessionName). The result is the
// canonical leo- form; stored names are not required to carry the prefix.
func NormalizeAgentName(raw string) (string, error) {
	body := strings.ToLower(strings.TrimSpace(raw))
	body = strings.TrimPrefix(body, agentNamePrefix)
	if body == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidAgentName)
	}
	if !charsetRe.MatchString(body) {
		return "", fmt.Errorf("%w: %q has invalid characters (allowed: a-z, 0-9, dash)", ErrInvalidAgentName, raw)
	}
	// Collapse runs of dashes and trim leading/trailing dashes.
	for strings.Contains(body, "--") {
		body = strings.ReplaceAll(body, "--", "-")
	}
	body = strings.Trim(body, "-")
	if body == "" {
		return "", fmt.Errorf("%w: %q reduces to empty after normalization", ErrInvalidAgentName, raw)
	}
	name := agentNamePrefix + body
	if len(name) > maxAgentNameLength {
		name = name[:maxAgentNameLength]
		name = strings.TrimRight(name, "-")
	}
	return name, nil
}

// renameTargetName validates a rename target and returns it as given (slugged,
// but without forcing the leo- prefix): records are keyed by the name the user
// chose, exactly as spawn --name does. The tmux session name keeps its own
// leo- derivation via SessionName.
func renameTargetName(raw string) (string, error) {
	norm, err := NormalizeAgentName(raw)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), agentNamePrefix) {
		return norm, nil
	}
	return strings.TrimPrefix(norm, agentNamePrefix), nil
}
