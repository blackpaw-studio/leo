// Pure helpers for what the daemon's state snapshots drive: delegation
// enforcement and the roster band. Nothing here touches the mods API.

// The system-prompt section the mod adds while delegation is on.
export const DELEGATION_SECTION_ID = 'leo-bridge:leo-delegation'

// What a native agent call is told while delegation is on.
export const AGENT_DENY_TEXT =
  'Delegation is on: use leo_dispatch(role: …) — see the leo-delegation section. Native agents are allowed only after a leo_dispatch fails.'

// The leo MCP tool whose failure lets native agents through.
export const LEO_DISPATCH_TOOL = 'mcp__leo__leo_dispatch'

// How long after session.start the mod waits for a first state before it
// stops enforcing delegation (the daemon may be down or too old).
export const STATE_WAIT_MS = 10_000

// Why native agents are let through while delegation is on.
export const FALLBACK_NO_STATE = 'no state from leo'
export const FALLBACK_STREAM_DOWN = 'leo bridge down'
export const FALLBACK_DISPATCH_FAILED = 'leo_dispatch failed'

const RUNNING = 'running'
const IDLE = 'idle'
const TERMINAL = ['done', 'failed', 'timeout', 'canceled', 'closed', 'released']

/**
 * Parses a state line's value: delegation policy and dispatches, every field
 * coerced to its type. Null when it is not a state line at all.
 * @param {unknown} value
 */
export function parseState(value) {
  if (!isRecord(value) || value.op !== 'state') return null
  const deleg = isRecord(value.delegation) ? value.delegation : {}
  const dispatches = Array.isArray(value.dispatches) ? value.dispatches : []
  return {
    delegation: {
      enabled: deleg.enabled === true,
      section: str(deleg.section),
      hideAgents: Array.isArray(deleg.hide_agents) ? deleg.hide_agents.filter((x) => typeof x === 'string') : [],
    },
    dispatches: dispatches.filter((d) => isRecord(d) && typeof d.id === 'string' && d.id !== '').map(parseDispatch),
  }
}

/**
 * parseState over one stream line; null for anything but a state line
 * (commands, garbage), which the command path then handles.
 * @param {string} line
 */
export function parseStateLine(line) {
  try {
    return parseState(JSON.parse(line))
  } catch {
    return null
  }
}

// The $.store key prefix for what the model was last told about delegation.
export const TOLD_KEY_PREFIX = 'told:'

/**
 * The stored form of what session was told: the policy as the model read
 * it, and when it was written (at, ms) so stale entries can be pruned.
 */
export function toldEntry(session, delegation, at) {
  return { session, enabled: delegation.enabled, section: delegation.section, at }
}

/**
 * What a stored entry says session was told, or null when it holds nothing
 * for that session (another session's, a junk value, none).
 * @param {unknown} value
 * @param {string} session
 */
export function toldFromEntry(value, session) {
  if (!isRecord(value) || value.session !== session || typeof value.section !== 'string') return null
  return { enabled: value.enabled === true, section: value.section }
}

// What the model was told before any state arrived: no delegation section.
export const NO_DELEGATION = Object.freeze({ enabled: false, section: '', hideAgents: [] })

function parseDispatch(d) {
  return {
    id: d.id,
    name: str(d.name),
    role: str(d.role),
    template: str(d.template),
    model: str(d.model),
    status: str(d.status),
    stalled: d.stalled === true,
    activeSeconds: num(d.active_seconds) ?? 0,
    tokensIn: num(d.tokens_in),
    tokensOut: num(d.tokens_out),
    costUsd: num(d.cost_usd),
  }
}

/**
 * Whether two delegation policies read the same to the model.
 * @param {{ enabled: boolean, section: string } | null | undefined} a
 * @param {{ enabled: boolean, section: string } | null | undefined} b
 */
export function sameDelegationText(a, b) {
  const on = (x) => x?.enabled === true && x.section !== ''
  if (on(a) !== on(b)) return false
  return !on(a) || a.section === b.section
}

/**
 * The note that tells the model delegation changed after its system prompt
 * was composed: the engine keeps the prompt it composed first, so a changed
 * section reaches the model only this way.
 * @param {{ enabled: boolean, section: string }} delegation
 */
export function delegationNote(delegation) {
  if (delegation.enabled && delegation.section !== '') {
    return 'leo: delegation settings changed; this replaces the leo-delegation section of your system prompt.\n\n' + delegation.section
  }
  return 'leo: delegation is now OFF. Ignore the leo-delegation section of your system prompt; native agents are allowed.'
}

/**
 * The agent type without a plugin prefix (`plugin:name` → `name`).
 * @param {string} name
 */
export function bareAgentName(name) {
  const i = name.lastIndexOf(':')
  return i < 0 ? name : name.slice(i + 1)
}

/**
 * Whether delegation keeps the model from agent type `name`: delegation on,
 * not fallen back, and the type listed. An Agent call also may not use the
 * default type (`subagent_type` left out) or general-purpose.
 * @param {{ delegation: { enabled: boolean, hideAgents: string[] } } | null | undefined} state
 * @param {boolean} isFallback
 * @param {string} name
 * @param {boolean} isAgentCall
 */
export function isAgentHidden(state, isFallback, name, isAgentCall) {
  if (!state?.delegation.enabled || isFallback) return false
  const bare = bareAgentName(name)
  if (isAgentCall && (bare === '' || bare === 'general-purpose')) return true
  return state.delegation.hideAgents.includes(bare)
}

/** Whether a tool.call result is a failure: an error result or a refusal. */
export function isFailedCall(result) {
  return isRecord(result) && (result.isError === true || typeof result.deny === 'string')
}

export function isTerminal(status) {
  return TERMINAL.includes(status)
}

export function isRunning(dispatches) {
  return dispatches.some((d) => d.status === RUNNING)
}

/**
 * The status line: dispatch counts, then why delegation fell back, if it
 * did. Undefined clears the line.
 * @param {{ delegation: { enabled: boolean }, dispatches: Array<{ status: string }> } | null | undefined} state
 * @param {string | null} fallback
 */
export function statusLine(state, fallback) {
  const dispatches = state?.dispatches ?? []
  const running = dispatches.filter((d) => d.status === RUNNING).length
  const idle = dispatches.filter((d) => d.status === IDLE).length
  const parts = running + idle > 0 ? ['⇢ ' + running + ' running · ' + idle + ' idle'] : []
  if (fallback !== null && state?.delegation.enabled !== false) parts.push('native agents allowed: ' + fallback)
  return parts.length > 0 ? parts.join(' · ') : undefined
}

/**
 * Working time shown for a dispatch: the snapshot's, plus the time since it
 * arrived while the dispatch runs.
 */
export function elapsedSeconds(d, receivedAt, now) {
  const extra = d.status === RUNNING ? Math.max(0, now - receivedAt) / 1000 : 0
  return d.activeSeconds + extra
}

/** `m:ss`, or `h:mm:ss` from an hour. */
export function formatElapsed(seconds) {
  const total = Math.max(0, Math.floor(seconds))
  const s = String(total % 60).padStart(2, '0')
  if (total < 3600) return Math.floor(total / 60) + ':' + s
  return Math.floor(total / 3600) + ':' + String(Math.floor((total % 3600) / 60)).padStart(2, '0') + ':' + s
}

function formatTokens(n) {
  if (n === undefined) return '–'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1000) return (n / 1000).toFixed(1) + 'k'
  return String(n)
}

function glyph(status) {
  if (status === IDLE || status === 'settling') return '⏸'
  if (status === 'done' || status === 'closed') return '✓'
  if (['failed', 'canceled', 'timeout'].includes(status)) return '✗'
  if (status === 'queued') return '…'
  return '⟳'
}

/**
 * One roster row's text (the Cancel button aside).
 * @param {ReturnType<typeof parseDispatch>} d
 * @param {number} receivedAt when the snapshot arrived (ms)
 * @param {number} now (ms)
 */
export function rosterRowText(d, receivedAt, now) {
  const parts = [
    glyph(d.status) + ' ' + (d.name || d.role || d.template || d.id),
    d.model,
    d.status + (d.stalled ? ' · stalled' : ''),
    formatElapsed(elapsedSeconds(d, receivedAt, now)),
    formatTokens(d.tokensIn) + '/' + formatTokens(d.tokensOut),
  ]
  if (d.costUsd !== undefined) parts.push('$' + d.costUsd.toFixed(2))
  return parts.filter((p) => p !== '').join('  ')
}

/** The request report that cancels dispatch id. */
export function cancelRequest(id) {
  return { type: 'request', op: 'dispatch.cancel', dispatch_id: id }
}

function str(v) {
  return typeof v === 'string' ? v : ''
}

function num(v) {
  return typeof v === 'number' && Number.isFinite(v) ? v : undefined
}

function isRecord(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}
