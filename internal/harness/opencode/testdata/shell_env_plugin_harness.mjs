// Runs the leo shell env plugin's shell.env hook the way opencode does: the
// hook edits an initially empty env, and a shell command's effective env is
// {...process.env, ...hook env}. Prints that effective env as JSON.
import { pathToFileURL } from "node:url"

const processEnv = { TMUX: "/tmp/leo-tmux/leo,1,0", TMUX_PANE: "%3", TMUX_TMPDIR: "/tmp/leo-tmux", PATH: "/usr/bin" }
const mod = await import(pathToFileURL(process.argv[2]).href)
const hooks = await Object.values(mod)[0]({})
const output = { env: {} }
await hooks["shell.env"]({ cwd: "/work" }, output)
console.log(JSON.stringify({ ...processEnv, ...output.env }))
