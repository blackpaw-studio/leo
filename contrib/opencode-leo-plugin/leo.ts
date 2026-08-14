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
 * Config comes from the environment, read once when the plugin loads (a token
 * rotation therefore needs an opencode restart, not just a new env value):
 *   LEO_URL          base URL of the Leo daemon, e.g. http://host.docker.internal:8370
 *   LEO_TOKEN        bearer token from `leo client add` — it MUST be an api_clients
 *                    token, not agent.token: the daemon stamps the sender identity
 *                    (and with it the reply address) only for scoped clients
 *   LEO_TARGET       name of the Leo agent this container may message
 *   LEO_CLIENT_NAME  this container's identity, matching its `api_clients` entry
 */

const REQUIRED_ENV = ["LEO_URL", "LEO_TOKEN", "LEO_TARGET", "LEO_CLIENT_NAME"] as const

/**
 * Generous relative to Leo's fast path (a live claude target is typed into its
 * pane in ~2s) because a suspended target is resumed first. A timeout here
 * means "no answer yet", never "not delivered" — see the error text below.
 * Override with LEO_TIMEOUT_MS.
 */
const DEFAULT_TIMEOUT_MS = 60_000

type LeoConfig = {
  readonly url: string
  readonly token: string
  readonly target: string
  readonly clientName: string
  readonly timeoutMs: number
}

type ConfigError = { readonly error: string }

const isError = (c: LeoConfig | ConfigError): c is ConfigError => "error" in c

/**
 * safeBaseUrl returns scheme://host(/path) with any userinfo removed, so a
 * `LEO_URL` written as http://user:pass@host can never surface credentials in
 * an error string handed back to the model.
 */
function safeBaseUrl(raw: string): string | ConfigError {
  let parsed: URL
  try {
    parsed = new URL(raw.trim())
  } catch {
    // Deliberately does not echo the value: an unparseable LEO_URL may still
    // contain credentials, and this string reaches the model.
    return { error: "LEO_URL is not a valid URL" }
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    return { error: `LEO_URL must be http or https, got ${parsed.protocol}` }
  }
  const path = parsed.pathname.replace(/\/+$/, "")
  return `${parsed.protocol}//${parsed.host}${path}`
}

/**
 * readConfig returns the config or a single human-readable error. Bad config is
 * reported through the tool result rather than by refusing to load: a tool that
 * silently fails to appear is far harder to diagnose than one that says exactly
 * what is wrong.
 */
export function readConfig(env: Record<string, string | undefined>): LeoConfig | ConfigError {
  const missing = REQUIRED_ENV.filter((key) => !env[key]?.trim())
  if (missing.length > 0) {
    return { error: `missing ${missing.join(", ")}` }
  }
  const url = safeBaseUrl(env.LEO_URL!)
  if (typeof url !== "string") return url

  const rawTimeout = env.LEO_TIMEOUT_MS?.trim()
  const timeoutMs = rawTimeout ? Number(rawTimeout) : DEFAULT_TIMEOUT_MS
  if (!Number.isFinite(timeoutMs) || timeoutMs <= 0) {
    return { error: "LEO_TIMEOUT_MS must be a positive number of milliseconds" }
  }

  return {
    url,
    token: env.LEO_TOKEN!.trim(),
    target: env.LEO_TARGET!.trim(),
    clientName: env.LEO_CLIENT_NAME!.trim(),
    timeoutMs,
  }
}

/** replyAddress is the identity Leo shows the agent: who, and which session to answer. */
const replyAddress = (clientName: string, sessionID: string) => `${clientName}#${sessionID}`

export const LeoMessaging: Plugin = async () => {
  const config = readConfig(process.env)

  return {
    tool: {
      message_leo: tool({
        description:
          `Send a message to the Leo agent "${isError(config) ? "(unconfigured)" : config.target}". ` +
          "It arrives as a new turn on their side, tagged with your name and session. Returns as soon " +
          "as it is delivered — any reply comes back later as a new message in this session, not as " +
          "this tool's result. Use it to report progress, ask a question, or hand off work.",
        args: {
          text: tool.schema.string().min(1).describe("The message body to deliver."),
        },
        async execute({ text }, context) {
          if (isError(config)) {
            throw new Error(
              `leo plugin is not configured: ${config.error}. Ask the operator to fix the container's environment.`,
            )
          }

          const from = replyAddress(config.clientName, context.sessionID)
          const endpoint = `${config.url}/web/agent/${encodeURIComponent(config.target)}/message`

          // context.abort cancels an in-flight send when the turn is
          // interrupted; the timeout bounds a daemon that accepts the
          // connection and then stalls. Tolerate a missing context.abort
          // rather than throwing a TypeError out of AbortSignal.any.
          const timeout = AbortSignal.timeout(config.timeoutMs)
          const signal = AbortSignal.any([context.abort, timeout].filter(Boolean) as AbortSignal[])

          let response: Response
          try {
            response = await fetch(endpoint, {
              method: "POST",
              headers: {
                "Content-Type": "application/json",
                Authorization: `Bearer ${config.token}`,
              },
              // Raw text: the daemon stamps "[message from <from>] " on it and
              // sanitizes the body itself (web.serveClient). Prefixing here too
              // would double it — the protocol has exactly one owner, and it is
              // the side that does not trust the other.
              body: JSON.stringify({ text, from }),
              signal,
            })
          } catch (err) {
            if (context.abort?.aborted) throw new Error("send cancelled")
            if (timeout.aborted) {
              throw new Error(
                `no response from Leo within ${config.timeoutMs / 1000}s. The message may already ` +
                  "have been delivered — do not resend it; report the timeout instead.",
              )
            }
            throw new Error(`could not reach Leo at ${config.url}: ${String(err)}`)
          }

          if (!response.ok) {
            // Leo's error bodies name other agents ("no such agent %q; running:
            // a, b, c"). This container is only entitled to know about its own
            // target, so the body goes to the operator's logs, not the model.
            const detail = await response.text().catch(() => "")
            console.error(`[leo] ${response.status} from ${endpoint}: ${detail.slice(0, 400)}`)
            throw new Error(
              `Leo rejected the message (HTTP ${response.status}). The operator can see why in the container logs.`,
            )
          }

          return {
            title: `→ ${config.target}`,
            output: `Delivered to ${config.target}. They may reply later into this session (${context.sessionID}).`,
            metadata: { target: config.target, from },
          }
        },
      }),
    },
  }
}
