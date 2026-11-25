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

  // What the daemon wants this session told (the agent's delegation policy
  // and brief): the same relay `leo dispatch context` makes for claude and
  // codex, asked here with a hook payload that opencode has no hook for.
  // Bounded like that command, so a dead daemon delays a message by at most
  // this long, and "" for any failure: the message then goes unchanged.
  const contextUrl = `http://127.0.0.1:${port}/api/agent/context`
  const contextTimeoutMs = 3000
  const askContext = async (payload) => {
    try {
      const res = await fetch(contextUrl, {
        method: "POST",
        headers,
        body: JSON.stringify({ token, payload }),
        signal: AbortSignal.timeout(contextTimeoutMs),
      })
      if (!res.ok) return ""
      const reply = await res.json()
      return typeof reply?.context === "string" ? reply.context : ""
    } catch {
      return ""
    }
  }

  // Sessions already told their SessionStart, and those compacted since
  // their last message. opencode has no startup/resume distinction and no
  // compaction hook that can add context, so both are synthesized from the
  // session's first message here and the session.compacted event below.
  const started = new Set()
  const compacted = new Set()

  // The context for one message, as one synthetic text part (what the model
  // sees as part of the user's turn, never shown as their words), or none.
  const contextFor = async (id, prompt) => {
    const source = started.has(id) ? (compacted.has(id) ? "compact" : "") : "startup"
    started.add(id)
    compacted.delete(id)
    const told = []
    if (source) told.push(await askContext({ hook_event_name: "SessionStart", source, session_id: id }))
    told.push(await askContext({ hook_event_name: "UserPromptSubmit", session_id: id, prompt }))
    const added = told.filter(Boolean).join("\n\n")
    return added ? { type: "text", synthetic: true, text: added } : undefined
  }

  // Sessions whose turn already reported its end through session.error;
  // the session.idle that follows must not report it again.
  const reported = new Set()

  return {
    "chat.message": async (input, output) => {
      reported.delete(input.sessionID)
      const prompt = text(output.parts)
      const context = await contextFor(input.sessionID, prompt)
      if (context) output.parts.push(context)
      await report({ hook_event_name: "UserPromptSubmit", session_id: input.sessionID, prompt })
    },
    event: async ({ event }) => {
      const id = event.properties?.sessionID ?? ""
      if (event.type === "session.compacted") {
        if (id) compacted.add(id)
      } else if (event.type === "session.error") {
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
