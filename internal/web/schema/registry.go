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
	// harness_options is excluded from the flat registry on every scope
	// below: it's a map rendered by the dedicated harness-options sub-form
	// (internal/web/schema/harnessform.go + components/harness_options.html),
	// not a flat field — same excluded-with-own-UI pattern as client.hosts.
	// harness itself IS registered (KindSelect over harness.Names()).
	// provider is excluded on every scope below: the standalone providers
	// management page was removed (endpoints are becoming the harness's own
	// concern); the underlying config field is still parsed/validated for
	// backward compatibility but has no web-UI surface anymore.
	// permission_mode/bypass_permissions/remote_control/agent/allowed_tools/
	// disallowed_tools/append_system_prompt moved to harness_options
	// (Validate() rejects the flat fields outright now — see
	// docs/configuration/harnesses.md); the web UI must not write config
	// Validate rejects. The harness_options forms live at
	// internal/web/schema/harnessform.go +
	// components/harness_options.html — see docs/configuration/harnesses.md's
	// Web UI section.
	SectionDefaults: {"harness_options", "provider",
		"permission_mode", "bypass_permissions", "remote_control",
		"allowed_tools", "disallowed_tools", "append_system_prompt"},
	SectionProcess: {"harness_options", "provider",
		"permission_mode", "bypass_permissions", "remote_control", "agent",
		"allowed_tools", "disallowed_tools", "append_system_prompt"},
	SectionTask: {"harness_options", "provider",
		"permission_mode", "allowed_tools", "disallowed_tools", "append_system_prompt"},
	SectionTemplate: {"harness_options", "provider",
		"permission_mode", "remote_control", "agent",
		"allowed_tools", "disallowed_tools", "append_system_prompt"},
	SectionSession: {"harness_options", "provider",
		"permission_mode", "agent",
		"allowed_tools", "disallowed_tools", "append_system_prompt"},
}

// --- Shared field builders -------------------------------------------------
// These capture patterns repeated across process/task/template/session
// sections so the registry below stays declarative and DRY.

func fHarness() Field {
	return Field{Key: "harness", Label: "Harness", Kind: KindSelect, Options: "harnesses", Group: "Harness",
		Help: "Coding agent CLI driving this scope; options below are harness-specific"}
}

func fModel(group string) Field {
	return Field{Key: "model", Label: "Model", Kind: KindDatalist, Group: group}
}

func fChannels(group string) []Field {
	return []Field{
		{Key: "channels", Label: "Channels", Group: group},
		{Key: "dev_channels", Label: "Dev channels", Group: group, Help: "Overrides channels when LEO_ENV=dev"},
	}
}

func fAddDirs(group string, advanced bool) Field {
	return Field{Key: "add_dirs", Label: "Additional directories", Group: group, Advanced: advanced,
		Help: "Comma-separated paths added to the workspace"}
}

func fEnv(group string, advanced bool) Field {
	return Field{Key: "env", Label: "Environment", Kind: KindEnvMap, Group: group, Advanced: advanced,
		Help: "KEY=VALUE, one per line"}
}

var registry = map[Section][]Field{
	SectionDefaults: {
		fHarness(),
		{Key: "model", Label: "Model", Kind: KindDatalist, Group: "Model"},
		{Key: "max_turns", Label: "Max turns", Group: "Limits"},
		{Key: "stale_resume_hours", Label: "Stale resume (hours)", Group: "Behavior", Advanced: true,
			Help: "Skip --resume when the stored session is older than this"},
		{Key: "idle_suspend_after", Label: "Idle suspend after", Kind: KindDuration, Group: "Behavior", Advanced: true,
			Help: "Auto-suspend idle ephemeral agents, e.g. \"2h\"; empty disables"},
	},

	SectionProcess: append([]Field{
		{Key: "enabled", Label: "Enabled", Group: "General"},
		{Key: "workspace", Label: "Workspace", Group: "General"},
		fHarness(),
		fModel("Model"),
		{Key: "max_turns", Label: "Max turns", Group: "Model"},
	}, append(fChannels("Channels"), []Field{
		{Key: "mcp_config", Label: "MCP config", Group: "Advanced", Advanced: true, Help: "Path to an MCP server config file"},
		fAddDirs("Advanced", true),
		fEnv("Advanced", true),
		{Key: "stale_resume_hours", Label: "Stale resume (hours)", Kind: KindNumber, Group: "Advanced", Advanced: true,
			Help: "Skip --resume when the stored session is older than this"},
	}...)...),

	SectionTask: append([]Field{
		{Key: "schedule", Label: "Schedule", Kind: KindCron, Group: "Schedule"},
		{Key: "timezone", Label: "Timezone", Group: "Schedule"},
		{Key: "enabled", Label: "Enabled", Group: "Schedule"},
		{Key: "prompt_file", Label: "Prompt file", Group: "Prompt"},
		fHarness(),
		fModel("Model"),
		{Key: "max_turns", Label: "Max turns", Group: "Model"},
		{Key: "timeout", Label: "Timeout", Kind: KindDuration, Group: "Execution"},
		{Key: "retries", Label: "Retries", Group: "Execution"},
		{Key: "silent", Label: "Silent", Group: "Execution"},
		{Key: "runtime", Label: "Runtime", Kind: KindSelect, Options: "runtimes", Group: "Execution",
			Help: "persistent injects into a supervised session instead of spawning claude -p"},
		{Key: "session", Label: "Session", Kind: KindSelect, Options: "sessions", Group: "Execution",
			Help: "named session from the sessions: block; empty derives one per task"},
		{Key: "lazy", Label: "Lazy", Group: "Execution", Help: "start the session on first firing instead of at boot"},
		{Key: "queue_max", Label: "Queue max", Group: "Execution", Help: "max queued firings; 0 = default (5)"},
		{Key: "channels", Label: "Channels", Group: "Notifications"},
		{Key: "dev_channels", Label: "Dev channels", Group: "Notifications", Help: "Overrides channels when LEO_ENV=dev"},
		{Key: "notify_on_fail", Label: "Notify on fail", Group: "Notifications"},
	}, Field{Key: "workspace", Label: "Workspace", Group: "Advanced", Advanced: true}, fEnv("Advanced", true)),

	SectionTemplate: append([]Field{
		{Key: "workspace", Label: "Workspace", Group: "General"},
		fHarness(),
		fModel("Model"),
		{Key: "max_turns", Label: "Max turns", Group: "Model"},
	}, append(fChannels("Channels"), []Field{
		{Key: "mcp_config", Label: "MCP config", Group: "Advanced", Advanced: true, Help: "Path to an MCP server config file"},
		fAddDirs("Advanced", true),
		fEnv("Advanced", true),
		{Key: "idle_suspend_after", Label: "Idle suspend after", Kind: KindDuration, Group: "Advanced", Advanced: true,
			Help: "Auto-suspend idle ephemeral agents, e.g. \"2h\"; empty disables"},
	}...)...),

	SectionSession: append([]Field{
		{Key: "workspace", Label: "Workspace", Group: "General"},
		fHarness(),
		fModel("Model"),
		{Key: "channels", Label: "Channels", Group: "Channels"},
	}, fAddDirs("Advanced", true), fEnv("Advanced", true),
		Field{Key: "idle_timeout", Label: "Idle timeout", Kind: KindDuration, Group: "Advanced", Advanced: true}),

	SectionClientHost: {
		{Key: "ssh", Label: "SSH", Group: "General", Help: "user@host"},
		{Key: "ssh_args", Label: "SSH args", Group: "General"},
		{Key: "leo_path", Label: "Leo path", Group: "General"},
		{Key: "tmux_path", Label: "Tmux path", Group: "General"},
	},

	SectionWeb: {
		{Key: "enabled", Label: "Enabled", Group: "General"},
		{Key: "port", Label: "Port", Group: "General",
			Warning: "Changing port or bind can lock you out of this UI; requires service restart"},
		{Key: "bind", Label: "Bind", Group: "General",
			Warning: "Changing port or bind can lock you out of this UI; requires service restart",
			Help:    "empty = 127.0.0.1 (loopback only)"},
		{Key: "allowed_hosts", Label: "Allowed hosts", Group: "General",
			Warning: "Removing your own address here will block your browser"},
	},

	SectionClient: {
		{Key: "default_host", Label: "Default host", Group: "General", Help: "default remote host for leo agent commands"},
	},
}
