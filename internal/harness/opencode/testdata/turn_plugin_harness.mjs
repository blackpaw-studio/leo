// Drives the leo turn plugin through one scripted opencode scenario (argv
// [3]) with a fake fetch and client, then prints every POST it made as JSON.
import { pathToFileURL } from "node:url"

const posts = []
globalThis.fetch = async (url, init) => {
  posts.push({ url: String(url), headers: init.headers, body: JSON.parse(init.body) })
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
const scenario = scenarios[process.argv[3] ?? "turn"]
const client = { session: { messages: async ({ path }) => ({ data: path.id === "ses_1" ? scenario.messages : [] }) } }

const mod = await import(pathToFileURL(process.argv[2]).href)
for (const plugin of Object.values(mod).filter((v) => typeof v === "function")) {
  const hooks = await plugin({ client, directory: "/tmp/ws", worktree: "/tmp/ws" })
  await hooks["chat.message"]?.(
    { sessionID: "ses_1" },
    { message: {}, parts: [{ type: "text", text: "reply OK" }, { type: "file", url: "x" }, { type: "text", text: "please" }, { type: "text", text: "<system>", synthetic: true }] },
  )
  for (const { type, error } of scenario.events) {
    await hooks.event?.({ event: { type, properties: error ? { sessionID: "ses_1", error } : { sessionID: "ses_1" } } })
  }
}
process.stdout.write(JSON.stringify(posts))
