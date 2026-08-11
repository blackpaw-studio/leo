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
// Leo writes that payload itself, so a malformed one means it has lost track
// of what this agent may do; refusing is the only safe reading, since the
// alternative silently restores full access.
func malformedPermissionsError(cmd *cobra.Command) error {
	return fmt.Errorf("%s: refusing to run — LEO_PERMISSIONS is set but malformed, so this agent's permissions cannot be determined", cmd.CommandPath())
}

// gateCommand refuses a CLI command whose leo MCP tool equivalent this
// agent's template denies. tool is the governing tool name — named in the
// error so the operator knows which deny_tools entry to change.
//
// Commands with no exact tool equivalent are mapped to the closest one:
// agent reset/prune/restart/rename/suspend are governed by leo_stop_agent,
// since denying "stop other agents" plainly means to deny disrupting them.
func gateCommand(cmd *cobra.Command, tool string) error {
	perms, ok := permissionsFromEnv()
	if !ok {
		return malformedPermissionsError(cmd)
	}
	if perms.DeniesTool(tool) {
		return fmt.Errorf("%s is not permitted for this agent (its template denies %s)", cmd.CommandPath(), tool)
	}
	return nil
}

// gateSpawnTemplate applies both the leo_spawn_agent denial and the can_spawn
// allowlist to a `leo agent spawn` invocation. A denied tool outranks the
// allowlist: it removes the capability outright.
func gateSpawnTemplate(cmd *cobra.Command, template string) error {
	if err := gateCommand(cmd, "leo_spawn_agent"); err != nil {
		return err
	}
	perms, ok := permissionsFromEnv()
	if !ok {
		return malformedPermissionsError(cmd)
	}
	if !perms.AllowsSpawn(template) {
		return fmt.Errorf("%s: not permitted to spawn template %q; allowed templates: %s",
			cmd.CommandPath(), template, strings.Join(perms.CanSpawn, ", "))
	}
	return nil
}
