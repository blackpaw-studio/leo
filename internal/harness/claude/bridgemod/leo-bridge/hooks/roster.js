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
    effort: str(d.effort),
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
 * The status line, for problems only: why delegation fell back, if it did
 * and delegation could be on. Undefined clears the line.
 * @param {{ delegation: { enabled: boolean } } | null | undefined} state
 * @param {string | null} fallback
 */
export function statusLine(state, fallback) {
  if (fallback === null || state?.delegation.enabled === false) return undefined
  return 'native agents allowed: ' + fallback
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

// How long a dispatch stays in the band after the mod first sees it end.
export const TERMINAL_LINGER_MS = 10_000

// The band's header rule width when the band's own is unknown.
const DEFAULT_BAND_WIDTH = 40
const BAND_TITLE = 'leo dispatches '
const LABEL_MAX = 16
// The band's left padding, in line with the engine's lines under the prompt;
// rows sit a further two in, beneath the header.
const BAND_PAD = '  '
const INDENT = BAND_PAD + '  '
const COLUMN_GAP = '  '
const STALLED = 'stalled'

/** The band's header: padding, its title, then a rule to the band's width. */
export function bandHeader(width) {
  const total = typeof width === 'number' && width > 0 ? width : DEFAULT_BAND_WIDTH
  return BAND_PAD + BAND_TITLE + '─'.repeat(Math.max(0, total - BAND_PAD.length - BAND_TITLE.length))
}

/**
 * When each terminal dispatch was first seen terminal (ms), as a new map:
 * seen is the last one, or null for the first snapshot after a (re)load,
 * whose terminal dispatches count as long gone. Dispatches no longer
 * terminal, or no longer in the snapshot, drop out.
 * @param {Map<string, number> | null} seen
 * @param {Array<{ id: string, status: string }>} dispatches
 * @param {number} now
 */
export function trackTerminal(seen, dispatches, now) {
  return new Map(
    dispatches
      .filter((d) => isTerminal(d.status))
      .map((d) => [d.id, seen === null ? -Infinity : (seen.get(d.id) ?? now)]),
  )
}

/** The dispatches the band shows: live ones, and terminal ones still lingering. */
export function shownDispatches(dispatches, seen, now) {
  return dispatches.filter((d) => !isTerminal(d.status) || now - (seen.get(d.id) ?? -Infinity) < TERMINAL_LINGER_MS)
}

/** When the last lingering terminal dispatch leaves the band (ms), or -Infinity. */
export function lingerEnd(seen) {
  return Math.max(-Infinity, ...[...seen.values()].map((at) => at + TERMINAL_LINGER_MS))
}

function glyph(status) {
  if (status === IDLE || status === 'settling') return '⏸'
  if (status === 'done' || status === 'closed') return '✓'
  if (['failed', 'canceled', 'timeout'].includes(status)) return '✗'
  if (status === 'queued') return '…'
  return '⟳'
}

// The glyph's Text props: the status color.
function glyphStyle(d) {
  if (d.stalled) return { color: 'warning' }
  if (['idle', 'settling', 'queued'].includes(d.status)) return { dimColor: true }
  if (d.status === 'done' || d.status === 'closed') return { color: 'success' }
  if (['failed', 'canceled', 'timeout'].includes(d.status)) return { color: 'error' }
  if (d.status === RUNNING) return { color: 'cyan' }
  return {}
}

// One row's column values, unpadded, in band order.
function rowCells(d, receivedAt, now) {
  return [
    { text: Array.from(d.name || d.role || d.template || d.id).slice(0, LABEL_MAX).join(''), align: 'left', gap: COLUMN_GAP },
    { text: [d.model, d.effort].filter((x) => x !== '').join(' · '), align: 'left', gap: COLUMN_GAP, dimColor: true },
    { text: formatElapsed(elapsedSeconds(d, receivedAt, now)), align: 'right', gap: COLUMN_GAP },
    { text: d.stalled ? STALLED : '', align: 'left', gap: ' ' },
    { text: formatTokens(d.tokensIn) + '/' + formatTokens(d.tokensOut), align: 'right', gap: COLUMN_GAP, dimColor: true },
    { text: d.costUsd === undefined ? '' : '$' + d.costUsd.toFixed(2), align: 'right', gap: COLUMN_GAP, dimColor: true },
  ]
}

/**
 * The band's rows, oldest first: each the dispatch id, whether it is live
 * (Cancel applies), and its styled segments, every column padded to the
 * widest value in the band. A column empty in every row is left out.
 * @param {Array<ReturnType<typeof parseDispatch>>} dispatches
 * @param {number} receivedAt when the snapshot arrived (ms)
 * @param {number} now (ms)
 */
export function rosterRows(dispatches, receivedAt, now) {
  const cells = dispatches.map((d) => rowCells(d, receivedAt, now))
  const widths = (cells[0] ?? []).map((_, i) => Math.max(...cells.map((row) => codePoints(row[i].text))))
  return dispatches.map((d, r) => ({
    id: d.id,
    isLive: !isTerminal(d.status),
    segments: [
      { text: INDENT },
      { text: glyph(d.status), ...glyphStyle(d) },
      ...cells[r].flatMap((cell, i) => (widths[i] === 0 ? [] : [{ text: i === 0 ? ' ' : cell.gap }, padded(cell, widths[i])])),
    ],
  }))
}

function padded(cell, width) {
  const pad = ' '.repeat(width - codePoints(cell.text))
  const text = cell.align === 'right' ? pad + cell.text : cell.text + pad
  return cell.dimColor ? { text, dimColor: true } : { text }
}

// Columns are cut and padded in code points, so a surrogate pair is never
// split. Wide (CJK, emoji) characters still count as one column each.
function codePoints(text) {
  return Array.from(text).length
}

/** A row's text as drawn. */
export function rowText(row) {
  return row.segments.map((s) => s.text).join('')
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
