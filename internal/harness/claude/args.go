package claude

import (
	"strconv"

	"github.com/blackpaw-studio/leo/internal/harness"
)

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

// agentArgs reproduces internal/agent.BuildTemplateArgs flag order exactly.
// Note: templates have no bypass-permissions fallback — this is enforced
// structurally: agentArgs ignores Options.BypassPermissions by design (unlike
// appendPermissionFlags, used by the other two kinds) rather than relying on
// callers to leave it false.
func agentArgs(spec harness.LaunchSpec, o Options) []string {
	var args []string
	args = append(args, "--model", spec.Model)
	args = appendChannelFlags(args, spec.Channels, spec.DevChannels)
	args = append(args, "--add-dir", spec.Workspace)
	for _, dir := range spec.AddDirs {
		args = append(args, "--add-dir", dir)
	}
	if o.RemoteControl {
		args = append(args, "--remote-control")
	}
	args = append(args, "--name", spec.Name)
	if o.PermissionMode != "" {
		args = append(args, "--permission-mode", o.PermissionMode)
	}
	if o.MCPConfigPath != "" {
		args = append(args, "--mcp-config", o.MCPConfigPath)
	}
	if o.AgentFile != "" {
		args = append(args, "--agent", o.AgentFile)
	}
	args = appendToolFlags(args, o)
	if o.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", o.AppendSystemPrompt)
	}
	args = append(args, o.LeoMCPArgs...)
	if spec.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(spec.MaxTurns))
	}
	if spec.Prompt != "" {
		args = append(args, spec.Prompt)
	}
	return args
}

// taskArgs reproduces internal/run.buildArgs flag order exactly.
func taskArgs(spec harness.LaunchSpec, o Options) []string {
	args := []string{
		"-p", spec.Prompt,
		"--model", spec.Model,
		"--max-turns", strconv.Itoa(spec.MaxTurns),
		"--output-format", "stream-json",
		"--verbose",
	}
	for _, ch := range spec.DevChannels {
		args = append(args, "--dangerously-load-development-channels", ch)
	}
	args = append(args, Claude{}.SessionArgs(spec.Session)...)
	args = appendPermissionFlags(args, o)
	if o.MCPConfigPath != "" {
		args = append(args, "--mcp-config", o.MCPConfigPath)
	}
	args = append(args, o.LeoMCPArgs...)
	args = append(args, "--add-dir", spec.Workspace)
	args = appendToolFlags(args, o)
	if o.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", o.AppendSystemPrompt)
	}
	return args
}
