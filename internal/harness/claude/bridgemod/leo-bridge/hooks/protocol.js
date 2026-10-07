// Pure helpers for the leo bridge protocol: framing, parsing, dedup bookkeeping,
// report shapes, and respawn backoff. Nothing here touches the mods API.

export const ACKED_CAP = 500
// Each claude process keeps the ids it acked under ACKED_KEY_PREFIX + its
// bridge key, restamped as it starts and after every turn. An entry left
// untouched this long belongs to a process long gone (a dispatch killed
// before its session ended); the next session.start of any bridged claude
// prunes it, so the store does not grow without end.
export const ACKED_KEY_PREFIX = 'acked:'
export const ACKED_MAX_AGE_MS = 7 * 24 * 60 * 60 * 1000
// Dispatch bridge keys start with this (consult.DispatchBridgeKey). A
// dispatch's claude never resumes once its session ends, so its entry goes
// with it.
export const DISPATCH_KEY_PREFIX = 'dispatch.'
// Caps the ids an acked entry records as handed to the engine; only the
// few a reload can catch in flight matter.
export const INFLIGHT_CAP = 50
export const BACKOFF_INITIAL_MS = 1000
// Kept short: a daemon restart cuts the stream, and the restarted daemon
// waits on the mod to reconnect for anything it queued meanwhile. A failed
// connect costs one short-lived `leo bridge` child.
export const BACKOFF_MAX_MS = 5000
export const BACKOFF_RESET_AFTER_MS = 60_000
// `leo bridge` exits with this when the daemon refuses this launch for good:
// another launch holds the key, or no daemon adopted the session. Retrying
// cannot help, so the mod stops bridging until it reloads. Mirrored by
// bridgemod.StaleLaunchExitCode on the Go side.
export const STALE_LAUNCH_EXIT_CODE = 3
// Waits before each retry of a report the daemon did not take, backing off
// to about a minute in all: long enough to ride out a daemon restart. Acks
// are idempotent (the mod dedups by id) and events carry ids, so a retry is
// always safe; later reports wait behind it, so order holds.
export const REPORT_RETRY_DELAYS_MS = [500, 1000, 2000, 4000, 8000, 15_000, 15_000, 15_000]
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
 * The acked ids in whatever the store held under an acked key: an
 * ackedEntry, or the bare list earlier mods wrote.
 * @param {unknown} value
 * @returns {string[]}
 */
export function ackedFromStore(value) {
  const ids = Array.isArray(value) ? value : isRecord(value) && Array.isArray(value.ids) ? value.ids : []
  return ids.filter((x) => typeof x === 'string')
}

/**
 * The store entry for a bridge key's acked ids, stamped with when it was
 * written, and the ids its current launch handed the engine and has not
 * settled yet (see withInflight).
 * @param {readonly string[]} ids
 * @param {number} at milliseconds since the epoch
 * @param {{ launch: string, ids: readonly string[] } | null} [inflight]
 * @returns {{ ids: string[], at: number, inflight?: { launch: string, ids: string[] } }}
 */
export function ackedEntry(ids, at, inflight = null) {
  const entry = { ids: [...ids], at }
  return inflight === null ? entry : { ...entry, inflight: { launch: inflight.launch, ids: [...inflight.ids] } }
}

/**
 * The entry with id appended to its acked ids and stamped now; what it
 * records in flight is kept.
 * @param {unknown} value the entry as stored
 * @param {string} id
 * @param {number} now
 */
export function withAcked(value, id, now) {
  return ackedEntry(appendAcked(ackedFromStore(value), id), now, inflightOf(value))
}

/**
 * The entry restamped now, holding what it held: a live process touches its
 * entry so another's prune never takes it for one long gone.
 * @param {unknown} value the entry as stored
 * @param {number} now
 */
export function touchedEntry(value, now) {
  return ackedEntry(ackedFromStore(value), now, inflightOf(value))
}

/**
 * The ids launch handed the engine and has not settled, per the entry;
 * none for another launch's record, or without a launch id.
 * @param {unknown} value the entry as stored
 * @param {string | null} launch
 * @returns {string[]}
 */
export function inflightIds(value, launch) {
  const inflight = inflightOf(value)
  return launch && inflight !== null && inflight.launch === launch ? inflight.ids : []
}

/**
 * The entry with id recorded as handed to launch's engine (isHandedOff) or
 * no longer; another launch's record is replaced, since a new process
 * starts with an empty prompt queue.
 * @param {unknown} value the entry as stored
 * @param {string} launch
 * @param {string} id
 * @param {boolean} isHandedOff
 * @param {number} now
 */
export function withInflight(value, launch, id, isHandedOff, now) {
  const held = inflightIds(value, launch).filter((x) => x !== id)
  const ids = isHandedOff ? appendAcked(held, id, INFLIGHT_CAP) : held
  return ackedEntry(ackedFromStore(value), now, { launch, ids })
}

function inflightOf(value) {
  if (!isRecord(value) || !isRecord(value.inflight)) return null
  const { launch, ids } = value.inflight
  if (typeof launch !== 'string' || !Array.isArray(ids)) return null
  return { launch, ids: ids.filter((x) => typeof x === 'string') }
}

/**
 * Whether an acked entry is past ACKED_MAX_AGE_MS at now. One without a
 * time cannot be dated, so it counts as stale.
 * @param {unknown} value
 * @param {number} now
 * @returns {boolean}
 */
export function isAckedEntryStale(value, now) {
  if (!isRecord(value) || typeof value.at !== 'number') return true
  return now - value.at > ACKED_MAX_AGE_MS
}

/**
 * Whether a session.end leaves the process for good: not a /clear or a
 * resume, after which the same process goes on under another session.
 * @param {unknown} reason
 * @returns {boolean}
 */
export function isFinalSessionEnd(reason) {
  return reason !== 'clear' && reason !== 'resume'
}

function isRecord(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
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
 * @param {boolean | undefined} busy whether a main-loop turn is running right
 *   now; undefined (left out, so the daemon keeps what it knows) when the
 *   mod cannot tell
 * @returns {{ type: 'hello', session_id: string, claude_version: string, busy?: boolean }}
 */
export function helloReport(sessionId, claudeVersion, busy) {
  const hello = { type: 'hello', session_id: sessionId, claude_version: claudeVersion }
  return busy === undefined ? hello : { ...hello, busy }
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

// The MCP tool a fork consult rides on: leo's MCP server registers as
// `leo`, so claude lists leo_consult under this name.
export const CONSULT_TOOL = 'mcp__leo__leo_consult'

/**
 * A count the engine reported, or zero for one it left out or garbled.
 * @param {unknown} value
 * @returns {number}
 */
function tokenCount(value) {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : 0
}

/**
 * The token counts a turn.complete report carries, from the engine's
 * TurnUsage. The engine leaves usage out when nothing counted (an
 * interrupt, an API error), so a turn without it counts as zero.
 * @param {unknown} usage
 * @returns {{ input: number, output: number, cache_read: number, cache_creation: number, model?: string }}
 */
export function turnTokens(usage) {
  const u = isRecord(usage) ? usage : {}
  const tokens = {
    input: tokenCount(u.input_tokens),
    output: tokenCount(u.output_tokens),
    cache_read: tokenCount(u.cache_read_input_tokens),
    cache_creation: tokenCount(u.cache_creation_input_tokens),
  }
  return typeof u.model === 'string' && u.model !== '' ? { ...tokens, model: u.model } : tokens
}

/**
 * How the mod answers a fork consult's tool call, from $.model.fork's
 * result: the reply under a header naming the model and its tokens
 * (in/out/cache-read), or a deny (an error result for the model) naming why
 * there is none.
 * @param {unknown} fork
 * @param {string} model the main loop's model, which the fork ran on
 * @returns {{ result: string } | { deny: string }}
 */
export function forkConsultAnswer(fork, model) {
  if (isRecord(fork) && fork.isAnswered === true && typeof fork.text === 'string') {
    const t = turnTokens(fork.usage)
    return { result: `[consult · fork/${model}] tokens ${t.input}/${t.output}/${t.cache_read}\n${fork.text}` }
  }
  const reason = isRecord(fork) && typeof fork.reason === 'string' ? fork.reason : 'no reply'
  const detail = isRecord(fork) && reason === 'api-error' ? ` (${fork.status ?? 'no status'} ${fork.error ?? 'unknown'})` : ''
  return { deny: `fork consult failed: ${reason}${detail}` }
}

// ---- observe reports -----------------------------------------------------

// The least time between two activity reports: activity is latest-wins, so
// what changes faster than this is replaced before it is sent.
export const ACTIVITY_MIN_INTERVAL_MS = 1000
// Caps every field of an observe report; the daemon clamps again.
export const MAX_SUMMARY_CHARS = 200

const PATH_TOOLS = ['Read', 'Edit', 'Write']
const PATTERN_TOOLS = ['Grep', 'Glob']
/**
 * The hostname of url, parsed as a URL so userinfo (which may itself hold
 * '@' or ':') never leaks; undefined when url has no host or does not parse.
 * @param {string} url
 * @returns {string | undefined}
 */
function urlHost(url) {
  try {
    return new URL(url).hostname || undefined
  } catch {
    return undefined
  }
}

/**
 * @param {unknown} value
 * @returns {string | undefined} the string capped at MAX_SUMMARY_CHARS, or
 *   undefined for an empty or non-string value
 */
export function capField(value) {
  if (typeof value !== 'string' || value.trim() === '') return undefined
  return value.length > MAX_SUMMARY_CHARS ? value.slice(0, MAX_SUMMARY_CHARS) : value
}

/**
 * path with the home directory spelled `~`.
 * @param {string} path
 * @param {string} home
 */
export function abbreviateHome(path, home) {
  const base = home.endsWith('/') ? home.slice(0, -1) : home
  if (base === '') return path
  if (path === base) return '~'
  return path.startsWith(base + '/') ? '~' + path.slice(base.length) : path
}

/**
 * The one-field summary of a tool call's input: never the input itself.
 * @param {string} tool
 * @param {unknown} input the tool's arguments
 * @param {string} home
 * @returns {string | undefined}
 */
function toolSummary(tool, input, home) {
  if (!isRecord(input)) return undefined
  if (tool === 'Bash') return typeof input.command === 'string' ? input.command.trim().split(/\s+/)[0] : undefined
  if (PATH_TOOLS.includes(tool)) return typeof input.file_path === 'string' ? abbreviateHome(input.file_path, home) : undefined
  if (PATTERN_TOOLS.includes(tool)) return typeof input.pattern === 'string' ? input.pattern : undefined
  if (tool === 'WebFetch') return typeof input.url === 'string' ? urlHost(input.url) : undefined
  return undefined
}

/**
 * What an activity or attention report says of a tool call: its name and a
 * summary of its input, each capped; keys without a value are left out.
 * @param {string} tool
 * @param {unknown} input
 * @param {string} home
 * @returns {{ tool?: string, summary?: string }}
 */
export function toolActivity(tool, input, home) {
  const fields = { tool: capField(tool), summary: capField(toolSummary(tool, input, home)) }
  return Object.fromEntries(Object.entries(fields).filter(([, v]) => v !== undefined))
}

/**
 * A session.compact trigger as a compact report names it: a plugin's
 * compaction (leo's own command) counts as manual; a precompute installs
 * nothing, so it is not reported (null).
 * @param {unknown} trigger
 * @returns {'manual' | 'auto' | null}
 */
export function compactTrigger(trigger) {
  if (trigger === 'auto') return 'auto'
  if (trigger === 'manual' || trigger === 'plugin') return 'manual'
  return null
}
