package claude

import "strings"

func appendChannelFlags(args []string, channels, devChannels []string) []string {
	for _, ch := range channels {
		args = append(args, "--channels", ch)
	}
	for _, ch := range devChannels {
		args = append(args, "--dangerously-load-development-channels", ch)
	}
	return args
}

func appendPermissionFlags(args []string, o Options) []string {
	if o.PermissionMode != "" {
		return append(args, "--permission-mode", o.PermissionMode)
	}
	if o.BypassPermissions {
		return append(args, "--dangerously-skip-permissions")
	}
	return args
}

func appendToolFlags(args []string, o Options) []string {
	if len(o.AllowedTools) > 0 {
		args = append(args, "--allowed-tools", strings.Join(o.AllowedTools, ","))
	}
	if len(o.DisallowedTools) > 0 {
		args = append(args, "--disallowed-tools", strings.Join(o.DisallowedTools, ","))
	}
	return args
}
