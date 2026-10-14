// Drives the leo turn plugin through one opencode turn and an abort with a
// fake fetch and client, then prints every POST it made as JSON.
import { pathToFileURL } from "node:url"

const posts = []
globalThis.fetch = async (url, init) => {
  posts.push({ url: String(url), headers: init.headers, body: JSON.parse(init.body) })
  return { ok: true, status: 200 }
}
const client = {
  session: {
    messages: async ({ path }) => ({
      data: path.id === "ses_1"
        ? [
            { info: { role: "user" }, parts: [{ type: "text", text: "reply OK" }] },
            { info: { role: "assistant" }, parts: [{ type: "reasoning", text: "thinking" }, { type: "text", text: "OK" }] },
          ]
        : [],
    }),
  },
}

const mod = await import(pathToFileURL(process.argv[2]).href)
const plugins = Object.values(mod).filter((v) => typeof v === "function")
for (const plugin of plugins) {
  const hooks = await plugin({ client, directory: "/tmp/ws", worktree: "/tmp/ws" })
  await hooks["chat.message"]?.(
    { sessionID: "ses_1" },
    { message: {}, parts: [{ type: "text", text: "reply OK" }, { type: "file", url: "x" }, { type: "text", text: "please" }, { type: "text", text: "<system>", synthetic: true }] },
  )
  await hooks.event?.({ event: { type: "session.status", properties: { sessionID: "ses_1" } } })
  await hooks.event?.({ event: { type: "session.idle", properties: { sessionID: "ses_1" } } })
  await hooks.event?.({ event: { type: "session.error", properties: { sessionID: "ses_1", error: { name: "MessageAbortedError" } } } })
  await hooks.event?.({ event: { type: "session.error", properties: { sessionID: "ses_1", error: { name: "UnknownError" } } } })
}
process.stdout.write(JSON.stringify(posts))
