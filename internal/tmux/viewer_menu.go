package tmux

import (
	"fmt"
	"strings"
)

// ViewerMenuBinding describes the command installed on prefix+L.
type ViewerMenuBinding struct {
	LeoPath    string
	ConfigPath string
}

func shellQuoteLiteral(s string) string {
	s = strings.ReplaceAll(s, "#", "##")
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func viewerMenuPOSIXQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

// ViewerMenuAction builds a display-menu command value. display-menu expands
// formats while installing the action and run-shell expands them again when
// selected, hence four hashes are required for each literal hash.
func ViewerMenuAction(leo, configPath, session, action string) string {
	command := fmt.Sprintf("%s --config %s dispatch viewer %s --session %s", viewerMenuPOSIXQuote(leo), viewerMenuPOSIXQuote(configPath), action, viewerMenuPOSIXQuote(session))
	command = strings.ReplaceAll(command, "\\", "\\\\")
	command = strings.ReplaceAll(command, "\"", "\\\"")
	command = strings.ReplaceAll(command, "$", "\\$")
	command = strings.ReplaceAll(command, "#", "####")
	return "run-shell \"" + command + "\""
}

// ViewerMenuBindingCommand is intentionally a tmux format string: session_name
// is quoted by tmux at invocation time, while known literals are POSIX quoted.
func ViewerMenuBindingCommand(binding ViewerMenuBinding) string {
	return fmt.Sprintf("if [ '#{client_control_mode}' != 1 ]; then %s --config %s dispatch viewer menu --session #{q:session_name}; fi", shellQuoteLiteral(binding.LeoPath), shellQuoteLiteral(binding.ConfigPath))
}

// InstallViewerMenuBinding installs (or refreshes) Leo's prefix+L binding.
func InstallViewerMenuBinding(tmuxPath string, binding ViewerMenuBinding) error {
	if binding.LeoPath == "" || binding.ConfigPath == "" {
		return fmt.Errorf("viewer menu binding requires executable and config paths")
	}
	return serverExecCommand(tmuxPath, Args("bind-key", "-T", "prefix", "L", "run-shell", ViewerMenuBindingCommand(binding))...).Run()
}
