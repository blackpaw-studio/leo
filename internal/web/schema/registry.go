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
	// harness/harness_options land in the web UI once the harness picker and
	// a per-adapter options editor ship (later plan); config/validation
	// support them today (see internal/config/harness.go).
	SectionDefaults: {"harness", "harness_options"},
	SectionProcess:  {"harness", "harness_options"},
	SectionTask:     {"harness", "harness_options"},
	SectionTemplate: {"harness", "harness_options"},
	SectionSession:  {"harness", "harness_options"},
}

// --- Shared field builders -------------------------------------------------
// These capture patterns repeated across process/task/template/session
// sections so the registry below stays declarative and DRY.

func fModel(group string) Field {
	return Field{Key: "model", Label: "Model", Kind: KindSelect, Options: "models", Group: group}
}

func fProvider(group string) Field {
	return Field{Key: "provider", Label: "Provider", Kind: KindSelect, Options: "providers", Group: group,
		Help: "Third-party Anthropic-compatible endpoint; empty = inherit"}
}

func fAgent(group string) Field {
	return Field{Key: "agent", Label: "Agent", Kind: KindSelect, Options: "agents", Group: group}
}

func fPermissions() []Field {
	return []Field{
		{Key: "permission_mode", Label: "Permission mode", Kind: KindSelect, Options: "permission_modes", Group: "Permissions"},
		{Key: "allowed_tools", Label: "Allowed tools", Group: "Permissions", Help: "Comma-separated tool names"},
		{Key: "disallowed_tools", Label: "Disallowed tools", Group: "Permissions", Help: "Comma-separated tool names"},
	}
}

func fChannels(group string) []Field {
	return []Field{
		{Key: "channels", Label: "Channels", Group: group},
		{Key: "dev_channels", Label: "Dev channels", Group: group, Help: "Overrides channels when LEO_ENV=dev"},
	}
}

func fAppendSystemPrompt(group string, advanced bool) Field {
	return Field{Key: "append_system_prompt", Label: "Append system prompt", Kind: KindTextarea, Group: group, Advanced: advanced}
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

	SectionProcess: append(append(append([]Field{
		{Key: "enabled", Label: "Enabled", Group: "General"},
		{Key: "workspace", Label: "Workspace", Group: "General"},
		fAgent("General"),
		fModel("Model"),
		fProvider("Model"),
		{Key: "max_turns", Label: "Max turns", Group: "Model"},
	}, fChannels("Channels")...), fPermissions()...), []Field{
		{Key: "mcp_config", Label: "MCP config", Group: "Advanced", Advanced: true, Help: "Path to an MCP server config file"},
		fAddDirs("Advanced", true),
		fEnv("Advanced", true),
		fAppendSystemPrompt("Advanced", true),
		{Key: "remote_control", Label: "Remote control", Group: "Advanced", Advanced: true},
		{Key: "bypass_permissions", Label: "Bypass permissions", Group: "Advanced", Advanced: true,
			Help: "Legacy switch; ignored when a permission mode is set"},
		{Key: "stale_resume_hours", Label: "Stale resume (hours)", Kind: KindNumber, Group: "Advanced", Advanced: true,
			Help: "Skip --resume when the stored session is older than this"},
	}...),

	SectionTask: append(append([]Field{
		{Key: "schedule", Label: "Schedule", Kind: KindCron, Group: "Schedule"},
		{Key: "timezone", Label: "Timezone", Group: "Schedule"},
		{Key: "enabled", Label: "Enabled", Group: "Schedule"},
		{Key: "prompt_file", Label: "Prompt file", Group: "Prompt"},
		fModel("Model"),
		fProvider("Model"),
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
	}, fPermissions()...), []Field{
		fAppendSystemPrompt("Advanced", true),
		{Key: "workspace", Label: "Workspace", Group: "Advanced", Advanced: true},
	}...),

	SectionTemplate: append(append(append([]Field{
		{Key: "workspace", Label: "Workspace", Group: "General"},
		fAgent("General"),
		fModel("Model"),
		fProvider("Model"),
		{Key: "max_turns", Label: "Max turns", Group: "Model"},
	}, fChannels("Channels")...), fPermissions()...), []Field{
		{Key: "mcp_config", Label: "MCP config", Group: "Advanced", Advanced: true, Help: "Path to an MCP server config file"},
		fAddDirs("Advanced", true),
		fEnv("Advanced", true),
		fAppendSystemPrompt("Advanced", true),
		{Key: "remote_control", Label: "Remote control", Group: "Advanced", Advanced: true},
		{Key: "idle_suspend_after", Label: "Idle suspend after", Kind: KindDuration, Group: "Advanced", Advanced: true,
			Help: "Auto-suspend idle ephemeral agents, e.g. \"2h\"; empty disables"},
	}...),

	SectionSession: append(append([]Field{
		{Key: "workspace", Label: "Workspace", Group: "General"},
		fAgent("General"),
		fModel("Model"),
		fProvider("Model"),
		{Key: "channels", Label: "Channels", Group: "Channels"},
	}, fPermissions()...), []Field{
		fAddDirs("Advanced", true),
		fEnv("Advanced", true),
		fAppendSystemPrompt("Advanced", true),
		{Key: "idle_timeout", Label: "Idle timeout", Kind: KindDuration, Group: "Advanced", Advanced: true},
	}...),

	SectionProvider: {
		{Key: "base_url", Label: "Base URL", Group: "General", Help: "Anthropic-Messages-compatible endpoint"},
		{Key: "api_key_env", Label: "API key env var", Group: "General", Help: "environment variable holding the API key"},
		{Key: "api_key_cmd", Label: "API key command", Group: "General", Help: "command that prints the API key"},
		{Key: "default_model", Label: "Default model", Group: "General"},
	},

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
