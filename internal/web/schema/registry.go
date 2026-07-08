package schema

// FieldsFor returns the ordered field list for a section's form.
func FieldsFor(s Section) []Field {
	return registry[s]
}

// Excluded names yaml keys deliberately absent from the web UI, per section.
// Every entry is a reviewed decision — the drift test enforces that a config
// field is either registered here or excluded here.
var Excluded = map[Section][]string{
	// hosts is rendered as its own add/remove entries UI, not a flat field.
	SectionClient: {"hosts"},
}

var registry = map[Section][]Field{
	SectionDefaults: {
		{Key: "model", Label: "Model", Kind: KindSelect, Options: "models", Group: "Model"},
		{Key: "provider", Label: "Provider", Kind: KindSelect, Options: "providers", Group: "Model",
			Help: "Third-party Anthropic-compatible endpoint; empty = Anthropic"},
		{Key: "max_turns", Label: "Max turns", Group: "Limits"},
		{Key: "permission_mode", Label: "Permission mode", Kind: KindSelect, Options: "permission_modes", Group: "Permissions"},
		{Key: "bypass_permissions", Label: "Bypass permissions", Group: "Permissions",
			Help: "Legacy switch; ignored when a permission mode is set"},
		{Key: "allowed_tools", Label: "Allowed tools", Group: "Permissions"},
		{Key: "disallowed_tools", Label: "Disallowed tools", Group: "Permissions"},
		{Key: "remote_control", Label: "Remote control", Group: "Behavior", Advanced: true},
		{Key: "append_system_prompt", Label: "Append system prompt", Kind: KindTextarea, Group: "Behavior", Advanced: true},
		{Key: "stale_resume_hours", Label: "Stale resume (hours)", Group: "Behavior", Advanced: true,
			Help: "Skip --resume when the stored session is older than this"},
		{Key: "idle_suspend_after", Label: "Idle suspend after", Kind: KindDuration, Group: "Behavior", Advanced: true,
			Help: "Auto-suspend idle ephemeral agents, e.g. \"2h\"; empty disables"},
	},
}
