package codex

// SupportsReadOnlySandbox reports that codex enforces LaunchSpec.ReadOnly
// with --sandbox read-only. Codex cannot deny reads, so the secret denylist
// is not enforced on codex runs (an accepted, documented gap).
func (Codex) SupportsReadOnlySandbox() bool { return true }
