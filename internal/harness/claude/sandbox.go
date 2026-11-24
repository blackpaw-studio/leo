package claude

import (
	"encoding/json"
	"fmt"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// SupportsReadOnlySandbox reports that claude enforces LaunchSpec.ReadOnly:
// Claude Code's sandbox plus permission deny rules, merged into --settings.
func (Claude) SupportsReadOnlySandbox() bool { return true }

// readOnlySettings is the --settings value for a granted cross-workspace
// read. The sandbox confines Bash and its children: writes are denied in
// every target workspace (Claude Code's default write scope includes the cwd
// and --add-dir, so they must be named), the secret denylist is unreadable,
// and there is no unsandboxed escape hatch. permissions.deny covers the
// built-in file tools the same way; deny rules hold in every permission mode,
// bypassPermissions included.
func readOnlySettings(ro *harness.ReadOnlySandbox) map[string]any {
	var denyWrite, denyRead, deny []string
	for _, ws := range ro.Workspaces {
		denyWrite = append(denyWrite, ws)
		// Permission rules use gitignore syntax, where a leading "//" is
		// an absolute path; ws is already absolute.
		deny = append(deny, fmt.Sprintf("Edit(/%s/**)", ws))
		for _, p := range harness.SecretDenylist() {
			for _, path := range []string{ws + "/" + p, ws + "/**/" + p} {
				denyRead = append(denyRead, path)
				deny = append(deny, fmt.Sprintf("Read(/%s)", path))
			}
		}
	}
	// The weakening booleans are pinned false: they are scalars, so this
	// --settings value beats a user-scope one (which --setting-sources user
	// still loads). User-scope arrays (allowWrite, allowRead,
	// excludedCommands, allowedDomains, allowUnixSockets) merge across scopes
	// and cannot be cleared from here; that is an accepted risk.
	return map[string]any{
		"sandbox": map[string]any{
			"enabled":                      true,
			"failIfUnavailable":            true,
			"allowUnsandboxedCommands":     false,
			"autoAllowBashIfSandboxed":     true,
			"enableWeakerNestedSandbox":    false,
			"allowNestedUserNamespaces":    false,
			"enableWeakerNetworkIsolation": false,
			"allowAppleEvents":             false,
			"filesystem": map[string]any{
				"disabled":  false,
				"denyWrite": denyWrite,
				"denyRead":  denyRead,
			},
			"network": map[string]any{
				"allowAllUnixSockets": false,
				"allowLocalBinding":   false,
			},
		},
		"permissions": map[string]any{"deny": deny},
	}
}

// appendReadOnly adds the read-only sandbox to args when spec asks for one:
// its settings, folded with any --settings already in args into one trailing
// --settings (claude honors only one), and --setting-sources user so the
// target repo's project and local settings (their hooks, sandbox exclusions
// and allow rules) never load. args holds only inline settings here, so the
// fold needs no spill path.
func appendReadOnly(args []string, spec harness.LaunchSpec) ([]string, error) {
	if spec.ReadOnly == nil {
		return args, nil
	}
	encoded, err := json.Marshal(readOnlySettings(spec.ReadOnly))
	if err != nil {
		return nil, fmt.Errorf("claude: encode read-only settings: %w", err)
	}
	args = append(args, "--setting-sources", "user")
	merged, err := MergeSettingsArgs(args, []string{settingsFlag, string(encoded)}, MergeOptions{})
	if err != nil {
		return nil, fmt.Errorf("claude: fold read-only settings: %w", err)
	}
	return merged, nil
}
