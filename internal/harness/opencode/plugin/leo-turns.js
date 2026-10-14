// leo-turns: relays this opencode session's turn boundaries to the leo
// daemon as agent hooks (the same POST /api/agent/hook body `leo dispatch
// report` sends for claude and codex), so leo can tell when a turn it
// injected has finished. Installed by leo through OPENCODE_CONFIG_CONTENT;
// inert unless leo launched this process as a supervised agent.
export const LeoTurns = async ({ client }) => {
  const token = process.env.LEO_ATTENTION_TOKEN
  const port = process.env.LEO_WEB_PORT
  if (!token || !/^[0-9]+$/.test(port ?? "") || process.env.LEO_DISPATCH_ID) return {}

  const url = `http://127.0.0.1:${port}/api/agent/hook`
  const headers = { "Content-Type": "application/json" }
  if (process.env.LEO_API_TOKEN) headers.Authorization = `Bearer ${process.env.LEO_API_TOKEN}`

  // Best-effort, like `leo dispatch report`: a daemon that is down must not
  // surface as an error inside the agent.
  const report = async (payload) => {
    try {
      await fetch(url, {
        method: "POST",
        headers,
        body: JSON.stringify({ token, payload }),
        signal: AbortSignal.timeout(5000),
      })
    } catch {}
  }

  const text = (parts) =>
    (parts ?? [])
      .filter((p) => p.type === "text" && !p.synthetic)
      .map((p) => p.text)
      .join("\n")

  const lastAssistantMessage = async (id) => {
    try {
      const res = await client.session.messages({ path: { id } })
      const messages = res?.data ?? []
      for (let i = messages.length - 1; i >= 0; i--) {
        if (messages[i].info?.role === "assistant") return text(messages[i].parts)
      }
    } catch {}
    return ""
  }

  return {
    "chat.message": async (input, output) => {
      await report({ hook_event_name: "UserPromptSubmit", session_id: input.sessionID, prompt: text(output.parts) })
    },
    event: async ({ event }) => {
      const id = event.properties?.sessionID ?? ""
      if (event.type === "session.idle") {
        await report({ hook_event_name: "Stop", session_id: id, last_assistant_message: await lastAssistantMessage(id) })
      } else if (event.type === "session.error" && event.properties?.error?.name === "MessageAbortedError") {
        await report({ hook_event_name: "Interrupt", session_id: id })
      }
    },
  }
}
