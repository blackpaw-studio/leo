package claude

import "github.com/blackpaw-studio/leo/internal/harness"

// processArgs reproduces internal/cli.buildProcessArgs flag order exactly.
func processArgs(spec harness.LaunchSpec, o Options) []string {
	var args []string
	args = append(args, "--model", spec.Model)
	args = appendChannelFlags(args, spec.Channels, spec.DevChannels)
	args = append(args, "--add-dir", spec.Workspace)
	for _, dir := range spec.AddDirs {
		args = append(args, "--add-dir", dir)
	}
	if o.RemoteControl {
		args = append(args, "--remote-control")
		if o.RemoteControlPrefix != "" {
			args = append(args, "--remote-control-session-name-prefix", o.RemoteControlPrefix)
		}
	}
	args = appendPermissionFlags(args, o)
	if o.MCPConfigPath != "" {
		args = append(args, "--mcp-config", o.MCPConfigPath)
	}
	args = append(args, o.LeoMCPArgs...)
	if o.AgentFile != "" {
		args = append(args, "--agent", o.AgentFile)
	}
	args = appendToolFlags(args, o)
	if o.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", o.AppendSystemPrompt)
	}
	return args
}

func agentArgs(spec harness.LaunchSpec, o Options) []string {
	panic("claude: agentArgs not yet implemented (plan task 4)")
}

func taskArgs(spec harness.LaunchSpec, o Options) []string {
	panic("claude: taskArgs not yet implemented (plan task 5)")
}
