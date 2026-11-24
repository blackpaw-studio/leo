package harness

// ReadOnlySandbox asks the adapter to run a launch read-only at the OS level
// in the listed workspaces: a granted cross-workspace read (target_agent).
// Workspaces holds the target workspace as configured and symlink-resolved,
// deduplicated; adapters deny writes in every one of them.
type ReadOnlySandbox struct {
	Workspaces []string
}

// ReadOnlySandboxer is optionally implemented by harnesses that can enforce
// ReadOnlySandbox. A harness without it cannot host a granted read, and leo
// refuses the dispatch.
type ReadOnlySandboxer interface {
	SupportsReadOnlySandbox() bool
}

// SupportsReadOnlySandbox reports whether h can enforce a read-only sandbox.
func SupportsReadOnlySandbox(h Harness) bool {
	s, ok := h.(ReadOnlySandboxer)
	return ok && s.SupportsReadOnlySandbox()
}

// secretDenylist is the built-in set of file-name patterns a granted read may
// not open inside the target workspace. Not configurable. Only adapters that
// can deny reads (claude) enforce it.
var secretDenylist = []string{".env", ".env.*", "*.local", "*.pem", "*.key", "id_*", ".netrc", ".npmrc", "Secrets.xcconfig"}

// SecretDenylist returns a copy of the built-in secret read denylist.
func SecretDenylist() []string {
	return append([]string(nil), secretDenylist...)
}
