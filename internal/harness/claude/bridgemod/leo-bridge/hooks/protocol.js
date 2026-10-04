// Pure helpers for the leo bridge protocol: framing, parsing, dedup bookkeeping,
// report shapes, and respawn backoff. Nothing here touches the mods API.

export const ACKED_CAP = 500
export const BACKOFF_INITIAL_MS = 1000
export const BACKOFF_MAX_MS = 30_000
export const BACKOFF_RESET_AFTER_MS = 60_000
// Waits before each retry of a report the daemon did not take. Acks are
// idempotent (the mod dedups by id), so a retry is always safe.
export const REPORT_RETRY_DELAYS_MS = [500, 1000, 2000, 4000]
// Caps a prompt or answer echoed in a report, keeping every report well
// under the daemon's body limit.
export const MAX_REPORT_TEXT_CHARS = 1_000_000

/**
 * A prompt or answer as a report carries it: strings capped at
 * MAX_REPORT_TEXT_CHARS, anything else dropped.
 * @param {unknown} value
 * @returns {string | undefined}
 */
export function reportText(value) {
  if (typeof value !== 'string') return undefined
  return value.length > MAX_REPORT_TEXT_CHARS ? value.slice(0, MAX_REPORT_TEXT_CHARS) : value
}

const OPS = ['deliver', 'compact', 'clear', 'interrupt']

/**
 * Appends a chunk to the carried partial line and splits off complete lines.
 * @param {string} carry text left over from earlier chunks (no newline)
 * @param {string} text the new chunk
 * @returns {{ lines: string[], carry: string }}
 */
export function splitLines(carry, text) {
  const parts = (carry + text).split('\n')
  const rest = parts[parts.length - 1] ?? ''
  const lines = parts
    .slice(0, -1)
    .map((line) => (line.endsWith('\r') ? line.slice(0, -1) : line))
    .filter((line) => line.trim() !== '')
  return { lines, carry: rest }
}

/**
 * Parses one JSONL command line.
 * A line without a usable id cannot be acked, so it is rejected outright;
 * a line with an id but a bad op or payload is returned for an ok:false ack.
 * @param {string} line
 * @returns {{ kind: 'command', command: object } | { kind: 'invalid', id: string, error: string } | { kind: 'garbage', error: string }}
 */
export function parseCommand(line) {
  let value
  try {
    value = JSON.parse(line)
  } catch (err) {
    return { kind: 'garbage', error: 'invalid JSON: ' + errorText(err) }
  }
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    return { kind: 'garbage', error: 'command is not an object' }
  }
  const id = value.id
  if (typeof id !== 'string' || id === '') {
    return { kind: 'garbage', error: 'command has no id' }
  }
  if (!OPS.includes(value.op)) {
    return { kind: 'invalid', id, error: 'unknown op: ' + String(value.op) }
  }
  if (value.op === 'deliver') {
    if (typeof value.text !== 'string') {
      return { kind: 'invalid', id, error: 'deliver: text must be a string' }
    }
    return { kind: 'command', command: { id, op: 'deliver', text: value.text, asUser: value.as_user === true } }
  }
  if (value.op === 'compact') {
    if (value.instructions !== undefined && typeof value.instructions !== 'string') {
      return { kind: 'invalid', id, error: 'compact: instructions must be a string' }
    }
    const instructions = value.instructions === '' ? undefined : value.instructions
    return { kind: 'command', command: { id, op: 'compact', instructions } }
  }
  return { kind: 'command', command: { id, op: value.op } }
}

/**
 * Returns a new acked-id list with id appended, keeping only the newest `cap`.
 * @param {readonly string[]} list
 * @param {string} id
 * @param {number} [cap]
 * @returns {string[]}
 */
export function appendAcked(list, id, cap = ACKED_CAP) {
  const without = list.filter((x) => x !== id)
  const next = [...without, id]
  return next.length > cap ? next.slice(next.length - cap) : next
}

/**
 * Normalizes whatever the store held under the acked key into a string list.
 * @param {unknown} value
 * @returns {string[]}
 */
export function ackedFromStore(value) {
  return Array.isArray(value) ? value.filter((x) => typeof x === 'string') : []
}

/**
 * Decides the wait before the next spawn and the backoff after that.
 * @param {number} current the backoff that would apply now
 * @param {number} livedMs how long the stream that just ended lasted
 * @returns {{ waitMs: number, next: number }}
 */
export function nextBackoff(current, livedMs) {
  const waitMs = livedMs > BACKOFF_RESET_AFTER_MS ? BACKOFF_INITIAL_MS : current
  return { waitMs, next: Math.min(waitMs * 2, BACKOFF_MAX_MS) }
}

/**
 * @param {string} sessionId
 * @param {string} claudeVersion
 * @param {boolean} busy whether a main-loop turn is running right now
 * @returns {{ type: 'hello', session_id: string, claude_version: string, busy: boolean }}
 */
export function helloReport(sessionId, claudeVersion, busy) {
  return { type: 'hello', session_id: sessionId, claude_version: claudeVersion, busy }
}

/** @returns {{ type: 'ack', id: string, ok: boolean, error?: string }} */
export function ackReport(id, ok, error) {
  return ok || error === undefined ? { type: 'ack', id, ok } : { type: 'ack', id, ok, error }
}

/** @returns {{ type: 'event', name: string }} */
export function eventReport(name, extra = {}) {
  return { type: 'event', name, ...extra }
}

/**
 * The id the daemon deduplicates an event by: the event name and the turn or
 * session it belongs to, so a retried report carries the same one. Undefined
 * when there is nothing stable to derive it from.
 * @param {string} name
 * @param {unknown} scope a turn id (turn events) or session id (session.end)
 * @returns {string | undefined}
 */
export function eventId(name, scope) {
  return typeof scope === 'string' && scope !== '' ? name + ':' + scope : undefined
}

/**
 * Describes how the bridge child ended, for the reconnect log line.
 * @param {{ code: number | null, signal: string | null } | undefined} result
 * @param {string} stderr the tail of what the child wrote to stderr
 * @returns {string}
 */
export function describeExit(result, stderr) {
  const tail = stderr.trim()
  if (result === undefined) return tail
  const how = result.signal ? 'signal ' + result.signal : 'exit ' + String(result.code)
  return tail ? how + ': ' + tail : how
}

/** @returns {string} */
export function errorText(err) {
  if (err instanceof Error) return err.message
  return String(err)
}
