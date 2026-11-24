package config

import (
	"fmt"
	"strings"
)

// ShareConfig is a template's opt-in to cross-workspace read grants: who may
// run a read-only dispatch in the workspace of an agent spawned from it (see
// docs/configuration/dispatches.md).
type ShareConfig struct {
	// ReadableBy lists the root callers allowed to dispatch read-only roles
	// with target_agent naming an agent of this template. Each entry is
	// "agent:<exact agent name>", "template:<template name>" or
	// "task:<task name>". No wildcards.
	ReadableBy []string `yaml:"readable_by,omitempty"`
}

// GrantKind is the prefix of a share.readable_by entry.
type GrantKind string

const (
	GrantAgent    GrantKind = "agent"
	GrantTemplate GrantKind = "template"
	GrantTask     GrantKind = "task"
)

// ParseGrant splits a share.readable_by entry into its kind and name,
// rejecting unprefixed entries, unknown prefixes, empty names, wildcards and
// remote agents.
func ParseGrant(entry string) (GrantKind, string, error) {
	prefix, name, ok := strings.Cut(entry, ":")
	if !ok {
		return "", "", fmt.Errorf("%q has no prefix; use agent:<name>, template:<name> or task:<name>", entry)
	}
	kind := GrantKind(prefix)
	switch kind {
	case GrantAgent, GrantTemplate, GrantTask:
	default:
		return "", "", fmt.Errorf("%q has unknown prefix %q; use agent:, template: or task:", entry, prefix)
	}
	if name == "" {
		return "", "", fmt.Errorf("%q names nothing after %q", entry, prefix+":")
	}
	if strings.ContainsAny(name, "*?[") {
		return "", "", fmt.Errorf("%q: wildcards are not allowed; name each caller", entry)
	}
	if kind == GrantAgent && strings.Contains(name, "@") {
		return "", "", fmt.Errorf("%q: remote agents (agent@host) cannot be granted", entry)
	}
	return kind, name, nil
}

// validateShare checks one template's share block and returns the error
// strings it produces (empty when valid). Names are not checked for
// existence: agents are named at spawn time.
func validateShare(name string, s ShareConfig) []string {
	var errs []string
	seen := make(map[string]bool, len(s.ReadableBy))
	for i, entry := range s.ReadableBy {
		if _, _, err := ParseGrant(entry); err != nil {
			errs = append(errs, fmt.Sprintf("templates.%s.share.readable_by[%d]: %v", name, i, err))
			continue
		}
		if seen[entry] {
			errs = append(errs, fmt.Sprintf("templates.%s.share.readable_by[%d]: duplicate entry %q", name, i, entry))
		}
		seen[entry] = true
	}
	return errs
}
