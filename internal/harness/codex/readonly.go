package codex

import "github.com/blackpaw-studio/leo/internal/harness"

// sandboxReadOnly is codex's read-only sandbox mode.
const sandboxReadOnly = "read-only"

// SupportsReadOnlySandbox reports that codex enforces LaunchSpec.ReadOnly
// with --sandbox read-only. Codex cannot deny reads, so the secret denylist
// is not enforced on codex runs (an accepted, documented gap).
func (Codex) SupportsReadOnlySandbox() bool { return true }

// readOnlyMode is the permission preset a launch runs under: read-only for a
// granted read, whatever the template's permission_mode (approve-for-me
// included, which would otherwise imply workspace-write), else mode.
func readOnlyMode(spec harness.LaunchSpec, mode string) string {
	if spec.ReadOnly != nil {
		return sandboxReadOnly
	}
	return mode
}

// writableRootsArgs is the writable_roots override for a launch; a granted
// read gets none, so nothing in the target workspace is writable even if
// codex's sandbox mode were later widened.
func writableRootsArgs(spec harness.LaunchSpec) []string {
	if spec.ReadOnly != nil {
		return nil
	}
	return sandboxWritableRootsArgs(spec.Workspace)
}
