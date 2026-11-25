// Drives the leo turn plugin through one scripted opencode scenario (argv
// [3]) with a fake fetch and client. A turn scenario prints every hook POST it
// made as a JSON array. A context scenario (a `steps` list) prints
// {posts, contexts, parts, timeouts, rejected}: the hook POSTs, the requests to
// /api/agent/context, and the parts each chat.message ended with, after the
// plugin's injection. Context requests go to a real HTTP server on a unix
// socket the harness binds and names in LEO_SOCKET, as the daemon's launch
// does (transport "socket"); "tcp" leaves LEO_SOCKET unset and "missing-socket"
// names a socket nobody listens on, so the plugin falls back to the fake fetch.
import http from "node:http"
import { mkdtempSync, rmSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { pathToFileURL } from "node:url"

const posts = []
const contexts = []
let contextMode = "reply"

// Deadlines the plugin asked for on context requests (3000 ms each). The
// slow-daemon scenario alone shortens them so a hung request ends in milliseconds,
// but the value the plugin asked for is what is recorded.
const contextDeadlineMs = 3000
const timeouts = []
// AbortSignal.timeout timers do not keep node alive, and a hung request must
// still be allowed to end by its deadline.
const keepAlive = setInterval(() => {}, 5)
const realTimeout = AbortSignal.timeout.bind(AbortSignal)
AbortSignal.timeout = (ms) => {
  if (ms !== contextDeadlineMs) return realTimeout(ms)
  timeouts.push(ms)
  return realTimeout(contextMode === "slow" ? 25 : ms)
}
// What the fake daemon answers a context request with: it names the event and
// source it was asked about, so the parts show which relay produced them.
const contextReply = (payload) => {
  if (contextMode === "empty") return ""
  const source = payload.source ? "/" + payload.source : ""
  return "ctx:" + payload.hook_event_name + source
}
// The daemon's socket: a real server, so the plugin's own http.request runs.
let socketServer
let socketDir
const startSocketServer = async () => {
  socketDir = mkdtempSync(join(tmpdir(), "lt-"))
  const path = join(socketDir, "d.sock")
  socketServer = http.createServer((req, res) => {
    let raw = ""
    req.on("data", (chunk) => (raw += chunk))
    req.on("end", () => {
      const body = JSON.parse(raw)
      contexts.push({ via: "socket", path: req.url, method: req.method, headers: req.headers, body })
      if (contextMode === "down") return req.socket.destroy()
      if (contextMode === "slow") return // never answers: the request ends by its deadline
      if (contextMode === "error") {
        res.statusCode = 500
        return res.end()
      }
      res.setHeader("Content-Type", "application/json")
      res.end(JSON.stringify({ context: contextReply(body.payload) }))
    })
  })
  await new Promise((resolve) => socketServer.listen(path, resolve))
  return path
}

globalThis.fetch = async (url, init) => {
  const request = { via: "tcp", url: String(url), headers: init.headers, body: JSON.parse(init.body) }
  if (request.url.endsWith("/api/agent/context")) {
    contexts.push(request)
    if (contextMode === "down") throw new Error("daemon down")
    if (contextMode === "slow") {
      // A daemon that never answers: the request ends only when its deadline passes.
      return new Promise((_, reject) => init.signal.addEventListener("abort", () => reject(init.signal.reason)))
    }
    if (contextMode === "error") return { ok: false, status: 500 }
    return { ok: true, status: 200, json: async () => ({ context: contextReply(request.body.payload) }) }
  }
  posts.push(request)
  return { ok: true, status: 200 }
}

const user = (text) => ({ info: { role: "user" }, parts: [{ type: "text", text }] })
const assistant = (text, error) => ({
  info: error ? { role: "assistant", error } : { role: "assistant" },
  parts: [{ type: "reasoning", text: "thinking" }, { type: "text", text }],
})
const aborted = { name: "MessageAbortedError", data: { message: "aborted" } }
const authError = { name: "ProviderAuthError", data: { providerID: "lmstudio", message: "bad key" } }

const scenarios = {
  turn: {
    messages: [user("reply OK"), assistant("OK")],
    events: [{ type: "session.status" }, { type: "session.idle" }],
  },
  abort: {
    messages: [user("reply OK"), assistant("partial", aborted)],
    events: [{ type: "session.error", error: aborted }, { type: "session.idle" }],
  },
  "session-error": {
    messages: [user("reply OK"), assistant("", { name: "UnknownError", data: { message: "boom" } })],
    events: [{ type: "session.error", error: { name: "UnknownError", data: { message: "boom" } } }, { type: "session.idle" }],
  },
  "message-error": {
    messages: [user("reply OK"), assistant("", authError)],
    events: [{ type: "session.idle" }],
  },
}
// Context scenarios: steps are chat messages and session events, in order.
const message = (sessionID, text = "hello") => ({ message: text, sessionID })
const event = (type, sessionID) => ({ event: type, sessionID })
const contextScenarios = {
  "ctx-tcp-only": { transport: "tcp", steps: [message("ses_1")] },
  "ctx-socket-missing": { transport: "missing-socket", steps: [message("ses_1")] },
  "ctx-startup-then-prompts": { steps: [message("ses_1"), message("ses_1")] },
  "ctx-compaction": { steps: [message("ses_1"), event("session.compacted", "ses_1"), message("ses_1"), message("ses_1")] },
  "ctx-two-sessions": { steps: [message("ses_1"), message("ses_2")] },
  "ctx-empty": { mode: "empty", steps: [message("ses_1")] },
  "ctx-daemon-down": { mode: "down", steps: [message("ses_1")] },
  "ctx-daemon-error": { mode: "error", steps: [message("ses_1")] },
  "ctx-daemon-slow": { mode: "slow", steps: [message("ses_1")] },
}
const contextScenario = contextScenarios[process.argv[3]]
contextMode = contextScenario?.mode ?? "reply"
const scenario = contextScenario ? { messages: [], events: [] } : scenarios[process.argv[3] ?? "turn"]
const client = { session: { messages: async ({ path }) => ({ data: path.id === "ses_1" ? scenario.messages : [] }) } }

const transport = contextScenario?.transport ?? "socket"
delete process.env.LEO_SOCKET
if (contextScenario && transport === "socket") process.env.LEO_SOCKET = await startSocketServer()
if (contextScenario && transport === "missing-socket") process.env.LEO_SOCKET = join(tmpdir(), "lt-nobody-listens.sock")

const mod = await import(pathToFileURL(process.argv[2]).href)
const parts = []
const rejected = []
const savedIDs = new Set()
let messageCount = 0
for (const plugin of Object.values(mod).filter((v) => typeof v === "function")) {
  const hooks = await plugin({ client, directory: "/tmp/ws", worktree: "/tmp/ws" })
  for (const step of contextScenario?.steps ?? []) {
    if (step.event) {
      await hooks.event?.({ event: { type: step.event, properties: { sessionID: step.sessionID } } })
      continue
    }
    // opencode gives the message an id before the hook runs, and refuses to
    // save any part the hook added without its own id (prt_ + 26 chars),
    // sessionID and messageID: a part that lacks them kills the turn.
    const messageID = `msg_${String(++messageCount).padStart(12, "0")}ABCDEFGHIJKLMN`
    const output = { message: { id: messageID }, parts: [{ type: "text", text: step.message }] }
    const input = { sessionID: step.sessionID }
    await hooks["chat.message"]?.(input, output)
    output.parts.slice(1).forEach((part) => {
      const missing = []
      if (!/^prt_[0-9a-f]{12}[0-9A-Za-z]{14}$/.test(part.id ?? "")) missing.push("id")
      if (part.sessionID !== input.sessionID) missing.push("sessionID")
      if (part.messageID !== messageID) missing.push("messageID")
      if (missing.length) rejected.push({ message: messageCount - 1, missing })
      else if (savedIDs.has(part.id)) rejected.push({ message: messageCount - 1, missing: ["unique id"] })
      else savedIDs.add(part.id)
    })
    parts.push(output.parts)
  }
  if (contextScenario) continue
  await hooks["chat.message"]?.(
    { sessionID: "ses_1" },
    { message: {}, parts: [{ type: "text", text: "reply OK" }, { type: "file", url: "x" }, { type: "text", text: "please" }, { type: "text", text: "<system>", synthetic: true }] },
  )
  for (const { type, error } of scenario.events) {
    await hooks.event?.({ event: { type, properties: error ? { sessionID: "ses_1", error } : { sessionID: "ses_1" } } })
  }
}
clearInterval(keepAlive)
if (socketServer) {
  socketServer.closeAllConnections()
  socketServer.close()
  rmSync(socketDir, { recursive: true, force: true })
}
process.stdout.write(JSON.stringify(contextScenario ? { posts, contexts, parts, timeouts, rejected } : posts))
