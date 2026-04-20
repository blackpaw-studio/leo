package tmux

// SocketName is the dedicated tmux server name Leo creates and attaches
// all its supervised processes and agents to. Using a dedicated socket
// keeps `tmux ls` on the user's personal server free of leo-* sessions
// — inspecting leo's sessions is an explicit `tmux -L leo ls`.
const SocketName = "leo"

// Args prepends the leo socket selector to a tmux subcommand's args so
// every Leo-issued tmux invocation targets the dedicated server. Use it
// as `exec.Command(tmuxPath, tmux.Args("new-session", "-d", ...)...)`.
// For SSH'd invocations, append the result after the remote tmux path:
// `ssh host tmux <tmux.Args("attach", "-t", name)...>`.
func Args(rest ...string) []string {
	out := make([]string, 0, len(rest)+2)
	out = append(out, "-L", SocketName)
	out = append(out, rest...)
	return out
}
