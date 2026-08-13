import { tool, type Plugin } from "@opencode-ai/plugin"

/**
 * Leo messaging for an opencode agent that Leo does not supervise.
 *
 * Exposes exactly one tool. The agent can message one Leo agent and do nothing
 * else — the restriction is structural, not a guardrail that has to be trusted.
 *
 * The reply address is the thing this plugin exists for. opencode's MCP client
 * passes no session identity to tool servers (`tools/call` carries only
 * `{name, arguments}`), so an MCP-based integration cannot tell Leo which
 * session to answer. A plugin tool receives `ToolContext.sessionID` on every
 * call, so the originating session travels with the message and the reply
 * lands there even if the user has since switched sessions.
 *
 * Config comes from the environment (all required):
 *   LEO_URL          base URL of the Leo daemon, e.g. http://host.docker.internal:8370
 *   LEO_TOKEN        bearer token; scope it to this client via `api_clients` in leo.yaml
 *   LEO_TARGET       name of the Leo agent this container may message
 *   LEO_CLIENT_NAME  this container's identity, matching its `api_clients` entry
 */

const REQUIRED_ENV = ["LEO_URL", "LEO_TOKEN", "LEO_TARGET", "LEO_CLIENT_NAME"] as const

type LeoConfig = {
  readonly url: string
  readonly token: string
  readonly target: string
  readonly clientName: string
}

/**
 * readConfig returns the config, or the list of missing variables. Missing
 * config is reported through the tool result rather than by refusing to load:
 * a tool that silently fails to appear is far harder to diagnose than one that
 * says exactly which variable is unset.
 */
function readConfig(env: Record<string, string | undefined>): LeoConfig | { missing: string[] } {
  const missing = REQUIRED_ENV.filter((key) => !env[key]?.trim())
  if (missing.length > 0) return { missing: [...missing] }
  return {
    url: env.LEO_URL!.trim().replace(/\/+$/, ""),
    token: env.LEO_TOKEN!.trim(),
    target: env.LEO_TARGET!.trim(),
    clientName: env.LEO_CLIENT_NAME!.trim(),
  }
}

/** replyAddress is the `from` Leo records: identity plus the session to answer. */
function replyAddress(clientName: string, sessionID: string): string {
  return `${clientName}#${sessionID}`
}

const SEND_TIMEOUT_MS = 15_000

export const LeoMessaging: Plugin = async () => {
  const config = readConfig(process.env)

  return {
    tool: {
      message_leo: tool({
        description:
          `Send a message to the Leo agent "${"missing" in config ? "(unconfigured)" : config.target}". ` +
          "It arrives as a new turn on their side, prefixed with your name. Returns as soon as it is " +
          "delivered — their reply comes back later as a new message in this session, not as this " +
          "tool's result. Use it to report progress, ask a question, or hand off work.",
        args: {
          text: tool.schema.string().min(1).describe("The message body to deliver."),
        },
        async execute({ text }, context) {
          if ("missing" in config) {
            throw new Error(
              `leo plugin is not configured: missing ${config.missing.join(", ")}. ` +
                "Ask the operator to set them on the container.",
            )
          }

          const from = replyAddress(config.clientName, context.sessionID)
          const endpoint = `${config.url}/web/agent/${encodeURIComponent(config.target)}/message`

          // context.abort lets opencode cancel an in-flight send when the turn
          // is interrupted; the timeout bounds a daemon that accepts the
          // connection and then stalls.
          const timeout = AbortSignal.timeout(SEND_TIMEOUT_MS)
          const signal = AbortSignal.any([context.abort, timeout])

          let response: Response
          try {
            response = await fetch(endpoint, {
              method: "POST",
              headers: {
                "Content-Type": "application/json",
                Authorization: `Bearer ${config.token}`,
              },
              body: JSON.stringify({ text, from }),
              signal,
            })
          } catch (err) {
            const reason = timeout.aborted ? `timed out after ${SEND_TIMEOUT_MS}ms` : String(err)
            throw new Error(`could not reach Leo at ${config.url}: ${reason}`)
          }

          if (!response.ok) {
            const detail = (await response.text().catch(() => "")).slice(0, 400)
            throw new Error(
              `Leo rejected the message (HTTP ${response.status})${detail ? `: ${detail}` : ""}`,
            )
          }

          return {
            title: `→ ${config.target}`,
            output: `Delivered to ${config.target}. They will reply into this session (${context.sessionID}).`,
            metadata: { target: config.target, from },
          }
        },
      }),
    },
  }
}
