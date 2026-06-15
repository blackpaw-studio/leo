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

// Target wraps a tmux session name so that `-t` resolution requires an exact
// match. Without the leading "=", tmux falls back to prefix matching (and then
// fnmatch), so a lookup for session "leo-leo" silently resolves to a different
// session whose name merely starts with it — e.g. "leo-leoterm". That misfire
// is severe: a has-session liveness probe reports a dead session as alive (so
// the supervisor never restarts it — the agent "vanishes" while still showing
// "running"), and a kill-session lands on the wrong agent entirely.
//
// Use Target for commands that take a target-SESSION: has-session,
// kill-session, rename-session (the old name), and attach-session. For
// commands that take a target-PANE (send-keys, capture-pane, paste-buffer) use
// PaneTarget instead — a bare "=name" does not resolve as a pane.
//
// Do not use either for `-s` (new/rename target NAME, a literal) or for ssh's
// own `-t`.
func Target(session string) string { return "=" + session }

// PaneTarget wraps a session name as an exact-match target-PANE: "=name:". The
// trailing ":" selects the session's current window and active pane while the
// "=" forces exact session matching. send-keys, capture-pane, and paste-buffer
// take a target-pane; passing a bare "=name" to them fails to resolve (the "="
// only binds to session matching inside a pane spec), and passing a bare "name"
// reintroduces the prefix-matching misfire Target documents. PaneTarget is the
// correct exact form for those three commands.
func PaneTarget(session string) string { return "=" + session + ":" }
