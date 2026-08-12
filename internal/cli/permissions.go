package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/blackpaw-studio/leo/internal/leotools"
	"github.com/spf13/cobra"
)

// The leo CLI is on every agent's PATH and reaches the daemon over a Unix
// socket that authenticates nothing beyond the calling user, so an agent
// denied a leo MCP tool could otherwise just shell out and do the same thing.
// leo_skill's own instructions point at the CLI for several of these, which
// makes it the path a cooperative agent takes by default — not an evasion
// route a hostile one has to look for. Gating here applies one policy to both
// doors.
//
// Same trust model as the MCP layer: the payload rides an environment
// variable the agent's own process can clear. This is a guardrail, not a
// sandbox — see docs/configuration/permissions.md.

// permissionsFromEnv decodes the LEO_PERMISSIONS payload leo exports at spawn
// time. The bool reports whether it could be read; an unset value is a
// successful "no restriction" (a human shell, or an unrestricted template).
func permissionsFromEnv() (leotools.Permissions, bool) {
	raw := os.Getenv("LEO_PERMISSIONS")
	if raw == "" {
		return leotools.Permissions{}, true
	}
	var perms leotools.Permissions
	if err := json.Unmarshal([]byte(raw), &perms); err != nil {
		return leotools.Permissions{}, false
	}
	return perms, true
}

// malformedPermissionsError is returned when LEO_PERMISSIONS cannot be parsed.
// label names the refusing surface — a command path, or a description of the
// picker action that triggered it.
// Leo writes that payload itself, so a malformed one means it has lost track
// of what this agent may do; refusing is the only safe reading, since the
// alternative silently restores full access.
func malformedPermissionsError(label string) error {
	return fmt.Errorf("%s: refusing to run — LEO_PERMISSIONS is set but malformed, so this agent's permissions cannot be determined", label)
}

// gateCommand refuses a CLI command whose leo MCP tool equivalent this
// agent's template denies. tool is the governing tool name — named in the
// error so the operator knows which deny_tools entry to change.
//
// Commands with no exact tool equivalent are mapped to the closest one:
// agent reset/prune/restart/rename/suspend are governed by leo_stop_agent,
// since denying "stop other agents" plainly means to deny disrupting them.
func gateCommand(cmd *cobra.Command, tool string) error {
	return gateToolFor(cmd.CommandPath(), tool)
}

// gateToolFor is gateCommand keyed on a caller-supplied label instead of a
// cobra command, for surfaces that have no command to name — the attach
// picker's in-place actions run inside `leo attach`, not as their own verb.
func gateToolFor(label, tool string) error {
	perms, ok := permissionsFromEnv()
	if !ok {
		return malformedPermissionsError(label)
	}
	if perms.DeniesTool(tool) {
		return fmt.Errorf("%s is not permitted for this agent (its template denies %s)", label, tool)
	}
	return nil
}

// gateSpawnTemplate applies both the leo_spawn_agent denial and the can_spawn
// allowlist to a `leo agent spawn` invocation. A denied tool outranks the
// allowlist: it removes the capability outright.
func gateSpawnTemplate(cmd *cobra.Command, template string) error {
	return gateSpawnTemplateFor(cmd.CommandPath(), template)
}

// gateSpawnTemplateFor is gateSpawnTemplate keyed on a label — see gateToolFor.
func gateSpawnTemplateFor(label, template string) error {
	if err := gateToolFor(label, "leo_spawn_agent"); err != nil {
		return err
	}
	perms, ok := permissionsFromEnv()
	if !ok {
		return malformedPermissionsError(label)
	}
	if !perms.AllowsSpawn(template) {
		return fmt.Errorf("%s: not permitted to spawn template %q; allowed templates: %s",
			label, template, strings.Join(perms.CanSpawn, ", "))
	}
	return nil
}

// gateTemplateSwitch is the permission check for re-pointing an agent at
// another template, applied at both doors: `leo agent set-template` and the
// attach picker's template menu. A switch stops the agent and launches the
// target template, so it needs the disruption permission AND that template's
// can_spawn entry — gating only the former would let a template denied `codex`
// reach it by switching an agent into it.
func gateTemplateSwitch(label, template string) error {
	if err := gateToolFor(label, "leo_stop_agent"); err != nil {
		return err
	}
	return gateSpawnTemplateFor(label, template)
}
