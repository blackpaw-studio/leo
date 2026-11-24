package claude

// SupportsReadOnlySandbox reports that claude enforces LaunchSpec.ReadOnly:
// Claude Code's sandbox plus permission deny rules, merged into --settings.
func (Claude) SupportsReadOnlySandbox() bool { return true }
