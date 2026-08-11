// Package leotools holds the vocabulary shared between Leo's MCP tool
// registry (internal/mcp) and the config that constrains it
// (internal/config): the canonical tool-name list and the per-template
// Permissions type.
//
// It is deliberately a leaf — it imports nothing from Leo. internal/mcp
// transitively imports internal/config (via internal/consult), so config
// cannot import mcp to validate tool names; both import this instead.
//
// Permissions are a guardrail, not a security boundary. They are enforced
// inside the agent's own MCP server process, which runs as the same UID and
// holds the same daemon token as every other agent. They shape what an agent
// reaches for; they do not contain one that means to get around them.
package leotools

import (
	"path"
	"strings"
)

// SkillTool is the one tool that can never be denied. Every agent's system
// context unconditionally tells it to call leo_skill to operate Leo, so a
// template able to deny it could only produce a confusing dead end.
const SkillTool = "leo_skill"

// Names is the canonical, ordered list of the tool names Leo's MCP server
// registers. It is the source of truth for config validation;
// internal/mcp's registry test asserts the two match exactly in both
// directions, so this cannot drift from the definitions.
var Names = []string{
	SkillTool,
	"leo_clear",
	"leo_compact",
	"leo_interrupt",
	"leo_list_tasks",
	"leo_run_task",
	"leo_toggle_task",
	"leo_list_templates",
	"leo_spawn_agent",
	"leo_list_agents",
	"leo_stop_agent",
	"leo_send_message",
	"leo_consult",
}

// IsKnownTool reports whether name is a tool Leo's MCP server registers.
func IsKnownTool(name string) bool {
	for _, n := range Names {
		if n == name {
			return true
		}
	}
	return false
}

// Permissions is the per-template permission set. The zero value places no
// restriction at all, so templates without a permissions block behave exactly
// as they did before this type existed.
//
// DenyTools subtracts from the full tool surface. The three Can* fields are
// allowlists narrowing a specific tool's argument; an absent *or empty* list
// means unrestricted. Total denial is expressed by denying the tool itself
// (deny_tools: [leo_send_message]), not by an empty allowlist — that keeps
// the meaning stable across a yaml/json omitempty round trip, which would
// otherwise erase the difference between "[]" and "absent".
type Permissions struct {
	DenyTools  []string `yaml:"deny_tools,omitempty"  json:"deny_tools,omitempty"`
	CanMessage []string `yaml:"can_message,omitempty" json:"can_message,omitempty"`
	CanSpawn   []string `yaml:"can_spawn,omitempty"   json:"can_spawn,omitempty"`
	CanConsult []string `yaml:"can_consult,omitempty" json:"can_consult,omitempty"`
}

// IsZero reports whether the set carries no restriction. Empty-but-non-nil
// lists count as zero, so a config round trip that materializes "[]" does not
// start exporting a permissions payload for an unrestricted template.
func (p Permissions) IsZero() bool {
	return len(p.DenyTools) == 0 && len(p.CanMessage) == 0 &&
		len(p.CanSpawn) == 0 && len(p.CanConsult) == 0
}

// DeniesTool reports whether name is denied. Matching is exact: deny_tools
// deliberately does not glob, since a stray wildcard there would silently
// strip the whole tool surface. SkillTool is never denied.
func (p Permissions) DeniesTool(name string) bool {
	if name == SkillTool {
		return false
	}
	for _, denied := range p.DenyTools {
		if denied == name {
			return true
		}
	}
	return false
}

// AllowsMessage reports whether target is a permitted leo_send_message
// recipient. Note that the daemon resolves shorthand agent names but this
// check runs against the literal argument, so an allowlist of ["rocket"]
// rejects "rock" — fail-closed, with the allowed list surfaced in the error.
func (p Permissions) AllowsMessage(target string) bool {
	return allows(p.CanMessage, target)
}

// AllowsSpawn reports whether template is a permitted leo_spawn_agent target.
func (p Permissions) AllowsSpawn(template string) bool {
	return allows(p.CanSpawn, template)
}

// AllowsConsult reports whether template is a permitted leo_consult target.
func (p Permissions) AllowsConsult(template string) bool {
	return allows(p.CanConsult, template)
}

// allows applies one allowlist. An empty list is unrestricted. Entries match
// exactly or as a path.Match glob, so generated agent names stay addressable
// ("scout-*"). A malformed pattern never matches rather than erroring —
// config validation is where a bad pattern should be caught, and a parse
// failure at call time must not silently widen access.
func allows(list []string, value string) bool {
	if len(list) == 0 {
		return true
	}
	for _, entry := range list {
		if entry == value {
			return true
		}
		if ok, err := path.Match(entry, value); err == nil && ok {
			return true
		}
	}
	return false
}

// ValidPattern reports whether entry looks like a usable path.Match pattern.
// A malformed one is swallowed by allows (so a bad pattern can never widen
// access at call time) and then silently matches nothing but its own literal
// — tighter than the operator wrote, and reported nowhere at runtime. Config
// validation uses this to reject it up front instead.
//
// This is best-effort, not a full parse: path.Match has no compile step and
// only reports a bad construct when matching actually scans it, so an
// unterminated class ("team-[a") is caught while a reversed range
// ("team-[z-a]") is not. Both fail closed at runtime — matching less than
// intended, never more — so the gap costs a clear error message, not safety.
func ValidPattern(entry string) bool {
	_, err := path.Match(entry, "")
	return err == nil
}

// HasGlob reports whether entry contains path.Match metacharacters. Config
// validation uses it to decide which allowlist entries can be checked against
// defined template names and which must be accepted as patterns.
func HasGlob(entry string) bool {
	return strings.ContainsAny(entry, "*?[")
}
