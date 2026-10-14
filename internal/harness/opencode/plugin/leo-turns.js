// leo-turns: relays this opencode session's turn boundaries to the leo
// daemon as agent hooks (the same POST /api/agent/hook body `leo dispatch
// report` sends for claude and codex), so leo can tell when a turn it
// injected has finished: Stop for a clean end, Interrupt for an abort,
// StopFailure (with the error) when the turn failed. Installed by leo
// through OPENCODE_CONFIG_CONTENT; inert unless leo launched this process as
// a supervised agent.
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

  const isAbort = (error) => error?.name === "MessageAbortedError"
  const describe = (error) => {
    const message = error?.data?.message
    return message ? `${error.name}: ${message}` : (error?.name ?? "unknown error")
  }

  // The turn's last assistant message: its text, and its error when the
  // provider call failed (opencode records failures on the message).
  const lastAssistant = async (id) => {
    try {
      const res = await client.session.messages({ path: { id } })
      const messages = res?.data ?? []
      for (let i = messages.length - 1; i >= 0; i--) {
        if (messages[i].info?.role === "assistant") return { text: text(messages[i].parts), error: messages[i].info.error }
      }
    } catch {}
    return { text: "", error: undefined }
  }

  // The terminal hook for one turn: Stop only when it finished cleanly, so
  // a provider or auth failure is never reported as a successful turn.
  const ended = (id, last) => {
    if (!last.error) return { hook_event_name: "Stop", session_id: id, last_assistant_message: last.text }
    if (isAbort(last.error)) return { hook_event_name: "Interrupt", session_id: id }
    return { hook_event_name: "StopFailure", session_id: id, error: describe(last.error) }
  }

  // Sessions whose turn already reported its end through session.error;
  // the session.idle that follows must not report it again.
  const reported = new Set()

  return {
    "chat.message": async (input, output) => {
      reported.delete(input.sessionID)
      await report({ hook_event_name: "UserPromptSubmit", session_id: input.sessionID, prompt: text(output.parts) })
    },
    event: async ({ event }) => {
      const id = event.properties?.sessionID ?? ""
      if (event.type === "session.error") {
        // A session-less error is not this turn's; the turn's own failure
        // also lands on its assistant message, seen at session.idle.
        const error = event.properties?.error
        if (!error || !id) return
        reported.add(id)
        await report(ended(id, { text: "", error }))
      } else if (event.type === "session.idle") {
        if (reported.delete(id)) return
        await report(ended(id, await lastAssistant(id)))
      }
    },
  }
}
