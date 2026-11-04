// leo-shellenv: keeps TMUX and TMUX_PANE out of the shell commands this
// opencode session runs. Agents and dispatches live in panes of leo's tmux
// server, so a bare `tmux kill-server` in one of their shells would target
// that server. opencode itself, its MCP servers and the leo bridge keep the
// variables; only the shell tool's environment changes.
//
// opencode builds a shell command's env as {...process.env, ...hook env}, so
// deleting the keys here would strip nothing. They are blanked instead, and
// tmux ignores an empty TMUX (it falls back to the default socket under
// TMUX_TMPDIR, not leo's server).
export const LeoShellEnv = async () => ({
  "shell.env": async (_input, output) => {
    output.env.TMUX = ""
    output.env.TMUX_PANE = ""
  },
})
