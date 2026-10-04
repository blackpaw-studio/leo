// leo-bridge: runs inside Claude Code. It streams commands from the leo daemon
// (`leo bridge --agent <key> --launch <launch>`, one JSON command per stdout
// line), executes them in order, and reports acks and turn events back via
// `leo bridge report --agent <key> --launch <launch>` with the JSON report on
// stdin. It never decides policy. <key> is $LEO_BRIDGE_AGENT, the name leo
// routes by; <launch> is $LEO_BRIDGE_LAUNCH, the token leo minted for this
// claude process, so the daemon can refuse a process that is no longer the
// key's current one.
//
// Mods API rules this file follows: every `$.ns.method` call is spelled in
// full, event names are string literals, and `$` is only passed to top-level
// functions of this file. Module state resets on hot reload.

import {
  ACKED_KEY_PREFIX,
  ackedFromStore,
  ackReport,
  appendAcked,
  BACKOFF_INITIAL_MS,
  BACKOFF_RESET_AFTER_MS,
  describeExit,
  DISPATCH_KEY_PREFIX,
  errorText,
  eventId,
  eventReport,
  helloReport,
  inflightIds,
  isAckedEntryStale,
  isFinalSessionEnd,
  nextBackoff,
  parseCommand,
  REPORT_RETRY_DELAYS_MS,
  reportText,
  splitLines,
  STALE_LAUNCH_EXIT_CODE,
  touchedEntry,
  withAcked,
  withInflight,
} from './protocol.js'

const REPORT_TIMEOUT_MS = 15_000
const SESSION_END_WAIT_MS = 1000
const COMPACT_ATTEMPTS = 5
// How long a failed compact waits before its next try: briefly while the
// mod knows no turn runs, else until the running turn ends (bounded, in
// case the failure was something else).
const COMPACT_RETRY_MS = 250
const COMPACT_TURN_WAIT_MS = 60_000

// Ops whose engine call queues the work: the engine runs it even if this
// module is hot-reloaded before the call returns.
const HANDED_OFF_OPS = ['deliver', 'clear']

// Bridge identity, read from the environment at session.start; null = disabled.
let config = null
let isStarted = false
// Set once the daemon refuses this launch for good (a successor holds the
// key, or no daemon adopted this session): the mod neither reconnects nor
// reports until a reload resets it.
let isDormant = false

// The main loop's running turn id, or null when idle, and who waits for the
// turn to end. isTurnKnown is false until this module sees a turn start or
// end: a module loaded by a hot reload mid-turn cannot tell that turn runs.
let runningTurn = null
let isTurnKnown = false
let idleWaiters = []

// Whether a deliver's $.prompt.submit is in flight (it resolves once its
// turn starts), and the interrupts waiting for the turn it starts.
let isSubmitting = false
let turnStartWaiters = []

// Serial chains: reports keep per-process order; commands run one at a time;
// store entry rewrites never interleave (an interrupt settles off the
// command chain).
let reportChain = Promise.resolve()
let commandChain = Promise.resolve()
let storeChain = Promise.resolve()

// Ids queued or executing (guards redelivery while in flight) and the
// persisted list of ids already acked ok (null until loaded from $.store).
let pendingIds = new Set()
let ackedIds = null

// One log line per failure streak, so a down daemon doesn't flood the transcript.
let isStreamFailing = false
let isReportFailing = false

// The session id the last hello carried (null until the first hello).
let helloSessionId = null

function isBridging() {
  return config !== null && !isDormant
}

function ackedKey() {
  return ACKED_KEY_PREFIX + config.agent
}

// launch names this claude process (leo sets it per launch): the daemon
// takes a stream or report only from the key's current launch, and what an
// earlier load of the mod handed the engine is this process's only while
// it matches. Null (bridge disabled) unless all three are set.
async function readConfig($) {
  const bin = await $.env.get('LEO_BRIDGE_BIN')
  const agent = await $.env.get('LEO_BRIDGE_AGENT')
  const launch = await $.env.get('LEO_BRIDGE_LAUNCH')
  if (!bin || !agent || !launch) return null
  return { bin, agent, launch }
}

// `leo bridge` argv naming this process's key and launch; sub is a
// subcommand ('report') or none for the stream.
function bridgeArgv(...sub) {
  return [config.bin, 'bridge', ...sub, '--agent', config.agent, '--launch', config.launch]
}

// ---- reports -------------------------------------------------------------

// Sends one report, retrying with backoff while the daemon does not take it
// (down, restarting). Retrying is safe — acks are idempotent and events are
// state, not counters — and it happens inside the report chain, so later
// reports wait behind it and per-process order holds. The JSON goes on stdin:
// a prompt or answer can be far larger than argv allows.
async function sendReport($, report) {
  const argv = bridgeArgv('report')
  const body = JSON.stringify(report)
  for (let attempt = 0; ; attempt++) {
    const why = await tryReport($, argv, body)
    if (why === null) {
      isReportFailing = false
      return
    }
    if (attempt >= REPORT_RETRY_DELAYS_MS.length) {
      reportFailed($, why)
      return
    }
    await $.clock.sleep(REPORT_RETRY_DELAYS_MS[attempt])
  }
}

// One report attempt: null on success, else why it failed.
async function tryReport($, argv, body) {
  try {
    const result = await $.process.run(argv, { timeoutMs: REPORT_TIMEOUT_MS, stdin: body })
    if (result.exitCode === 0) return null
    return 'exit ' + result.exitCode + ': ' + result.stderr.trim()
  } catch (err) {
    return errorText(err)
  }
}

function reportFailed($, why) {
  if (isReportFailing) return
  isReportFailing = true
  $.ui.log('report failed: ' + why)
}

// Queues a report behind earlier ones. `build` may be async (e.g. reads usage);
// the returned promise settles once this report has been sent or has failed.
function enqueueReport($, build) {
  const sent = reportChain.then(() => buildAndSend($, build))
  reportChain = sent
  return sent
}

async function buildAndSend($, build) {
  try {
    const report = await build()
    if (report !== null) await sendReport($, report)
  } catch (err) {
    reportFailed($, errorText(err))
  }
}

// Only keys with a value: a report never carries an explicit undefined.
function defined(fields) {
  return Object.fromEntries(Object.entries(fields).filter(([, v]) => v !== undefined))
}

// The final assistant message is what a dispatch returns as its result.
// Usage is a nice-to-have: a turn.complete without it still ends the turn
// in the daemon, which a lost turn.complete would leave stuck busy.
async function turnCompleteReport($, e) {
  const fields = {
    event_id: eventId('turn.complete', e.turnId),
    message: reportText(e.answer),
    reason: e.isAborted === true ? 'aborted' : undefined,
  }
  try {
    const usage = await $.session.usage()
    return eventReport('turn.complete', defined({ ...fields, usage }))
  } catch (err) {
    $.ui.log('reading session usage failed: ' + errorText(err))
    return eventReport('turn.complete', defined(fields))
  }
}

async function sendHello($) {
  const sessionId = await $.session.id()
  const version = await $.session.version()
  helloSessionId = sessionId
  return helloReport(sessionId, version.version, isTurnKnown ? runningTurn !== null : undefined)
}

// Re-says hello when the session id moved since the last one (a /clear or
// resume keeps the process but starts a new session); null sends nothing.
async function helloIfSessionChanged($) {
  if (helloSessionId === null) return null
  const sessionId = await $.session.id()
  if (sessionId === helloSessionId) return null
  return sendHello($)
}

// ---- turn state ----------------------------------------------------------

function markRunning(turnId) {
  runningTurn = turnId
  isTurnKnown = true
  settleTurnStartWaiters(turnId)
}

function markIdle() {
  runningTurn = null
  isTurnKnown = true
  const waiters = idleWaiters
  idleWaiters = []
  waiters.forEach((resolve) => resolve())
}

function waitForIdle() {
  if (runningTurn === null) return Promise.resolve()
  return nextTurnEnd()
}

// Resolves at the next main-loop turn.complete, whatever the mod knows now.
function nextTurnEnd() {
  return new Promise((resolve) => {
    idleWaiters = [...idleWaiters, resolve]
  })
}

// Resolves with the id of the next main-loop turn to start, or null if the
// submit in flight settles without starting one.
function nextTurnStart() {
  return new Promise((resolve) => {
    turnStartWaiters = [...turnStartWaiters, resolve]
  })
}

function settleTurnStartWaiters(turnId) {
  const waiters = turnStartWaiters
  turnStartWaiters = []
  waiters.forEach((resolve) => resolve(turnId))
}

// ---- commands ------------------------------------------------------------

async function loadAcked($) {
  if (ackedIds !== null) return ackedIds
  try {
    const stored = ackedFromStore(await $.store.get(ackedKey()))
    // A concurrent remember may have set it while we awaited; keep the newer.
    if (ackedIds === null) ackedIds = stored
  } catch (err) {
    $.ui.log('reading acked ids failed: ' + errorText(err))
    if (ackedIds === null) ackedIds = []
  }
  return ackedIds
}

// Rewrites this key's store entry with change(entry as stored, now), one
// rewrite at a time. Every write reads the entry first, so a late write from
// a module a hot reload replaced changes only what it means to.
function updateEntry($, change) {
  const written = storeChain.then(() => rewriteEntry($, change))
  storeChain = written
  return written
}

async function rewriteEntry($, change) {
  try {
    const key = ackedKey()
    const now = await $.clock.now()
    await $.store.set(key, change(await $.store.get(key), now))
  } catch (err) {
    $.ui.log('saving acked ids failed: ' + errorText(err))
  }
}

// Records, before its engine call, that command id is being handed to the
// engine by this launch.
async function markHandedOff($, id) {
  await updateEntry($, (entry, now) => withInflight(entry, config.launch, id, true, now))
}

// Restamps this process's entry: it is alive, whatever another's prune
// makes of its age.
function touchEntry($) {
  return updateEntry($, touchedEntry)
}

// Whether an earlier load of the mod in this process handed command id to
// the engine and never settled it: a hot reload caught it in flight.
async function wasHandedOff($, id) {
  try {
    return inflightIds(await $.store.get(ackedKey()), config.launch).includes(id)
  } catch (err) {
    $.ui.log('reading acked ids failed: ' + errorText(err))
    return false
  }
}

// Records how command settled in one write: acked if ok, and no longer in
// flight. One write, so a reload never finds it neither acked nor in flight.
async function settleCommand($, command, ok) {
  const isHandedOff = HANDED_OFF_OPS.includes(command.op)
  if (ok) ackedIds = appendAcked(ackedIds ?? [], command.id)
  if (!ok && !isHandedOff) return
  await updateEntry($, (entry, now) => {
    const acked = ok ? withAcked(entry, command.id, now) : entry
    return isHandedOff ? withInflight(acked, config.launch, command.id, false, now) : acked
  })
}

// Deletes other processes' acked entries left unwritten past
// ACKED_MAX_AGE_MS. This process's own entry stays, however old: it is in
// use.
async function pruneAcked($) {
  try {
    const now = await $.clock.now()
    const own = ackedKey()
    const keys = (await $.store.keys()).filter((key) => key.startsWith(ACKED_KEY_PREFIX) && key !== own)
    for (const key of keys) {
      if (isAckedEntryStale(await $.store.get(key), now)) await $.store.delete(key)
    }
  } catch (err) {
    $.ui.log('pruning acked ids failed: ' + errorText(err))
  }
}

// A dispatch's claude never comes back once its session ends for good, so
// its acked entry goes with it; an agent's is kept for its next resume. The
// delete waits its turn on the store chain, so a rewrite still landing (the
// last turn's restamp) cannot bring the entry back.
function forgetDispatchAcked($, reason) {
  if (!config.agent.startsWith(DISPATCH_KEY_PREFIX) || !isFinalSessionEnd(reason)) return Promise.resolve()
  const deleted = storeChain.then(() => deleteEntry($))
  storeChain = deleted
  return deleted
}

async function deleteEntry($) {
  try {
    await $.store.delete(ackedKey())
  } catch (err) {
    $.ui.log('deleting acked ids failed: ' + errorText(err))
  }
}

async function runDeliver($, command) {
  const args = command.asUser ? { text: command.text, asUser: true } : { text: command.text }
  await markHandedOff($, command.id)
  isSubmitting = true
  try {
    const result = await $.prompt.submit(args)
    if (result && typeof result.drop === 'string') {
      settleTurnStartWaiters(null)
      return { ok: false, error: result.drop }
    }
    return { ok: true }
  } catch (err) {
    settleTurnStartWaiters(null)
    throw err
  } finally {
    isSubmitting = false
  }
}

async function runCompact($, command) {
  const args = command.instructions === undefined ? undefined : { instructions: command.instructions }
  let lastError = null
  for (let attempt = 0; attempt < COMPACT_ATTEMPTS; attempt++) {
    await waitForIdle()
    try {
      const result = await $.session.compact(args)
      if (result && typeof result.skip === 'string') return { ok: false, error: result.skip }
      return { ok: true }
    } catch (err) {
      lastError = err
      await waitBeforeCompactRetry($)
    }
  }
  return { ok: false, error: errorText(lastError) }
}

// compact rejects while a turn runs. One may have started between the idle
// check and the call (a just-delivered prompt), or be running unseen (this
// module was loaded by a hot reload mid-turn): either way the retry waits
// for that turn to end, not a fixed beat.
async function waitBeforeCompactRetry($) {
  const isIdle = isTurnKnown && runningTurn === null
  await Promise.race([nextTurnEnd(), $.clock.sleep(isIdle ? COMPACT_RETRY_MS : COMPACT_TURN_WAIT_MS)])
}

// A clear took only if the session it ran in ended: the id moves on. A hook
// may answer /clear in its place, and then nothing was cleared.
async function runClear($, command) {
  // Verified live on v2.1.289: a mod's command.run reaches the built-in
  // /clear (context wiped, session.end reason 'clear', the pump survives).
  await waitForIdle()
  const before = await $.session.id()
  await markHandedOff($, command.id)
  const result = await $.command.run({ command: 'clear', args: '' })
  if ((await $.session.id()) !== before) return { ok: true }
  const said = result && typeof result.text === 'string' ? result.text.trim() : ''
  return { ok: false, error: '/clear left the session as it was' + (said ? ': ' + said : '') }
}

// An interrupt aborts the running turn. One that lands while a submit is in
// flight with no turn running yet aborts the turn that submit starts, once
// it starts (or acks if the submit is dropped and starts none).
async function runInterrupt($) {
  const turnId = runningTurn ?? (isSubmitting ? await nextTurnStart() : null)
  if (turnId === null) return { ok: true }
  try {
    await $.turn.abort({ turnId })
    return { ok: true }
  } catch (err) {
    // The turn may have ended on its own between our read and the abort.
    if (runningTurn !== turnId) return { ok: true }
    return { ok: false, error: errorText(err) }
  }
}

async function execute($, command) {
  try {
    if (command.op === 'deliver') return await runDeliver($, command)
    if (command.op === 'compact') return await runCompact($, command)
    if (command.op === 'clear') return await runClear($, command)
    if (command.op === 'interrupt') return await runInterrupt($)
    return { ok: false, error: 'unknown op: ' + command.op }
  } catch (err) {
    return { ok: false, error: errorText(err) }
  }
}

async function handleCommand($, command) {
  const acked = await loadAcked($)
  if (acked.includes(command.id)) {
    enqueueReport($, () => ackReport(command.id, true))
    return
  }
  if (HANDED_OFF_OPS.includes(command.op) && (await wasHandedOff($, command.id))) {
    // The engine still holds it and runs it: running it again would run it
    // twice. It counts as accepted now.
    await settleCommand($, command, true)
    enqueueReport($, () => ackReport(command.id, true))
    return
  }
  const result = await execute($, command)
  await settleCommand($, command, result.ok)
  enqueueReport($, () => ackReport(command.id, result.ok, result.error))
}

// deliver/compact/clear run strictly in order on commandChain; interrupt runs
// at once so it can abort a turn that queued commands are waiting behind.
function enqueueCommand($, command) {
  if (pendingIds.has(command.id)) return
  pendingIds = new Set([...pendingIds, command.id])
  const run = () => handleCommand($, command)
  const started = command.op === 'interrupt' ? run() : commandChain.then(run)
  const finished = started
    .catch((err) => $.ui.log('command ' + command.id + ' failed: ' + errorText(err)))
    .then(() => {
      pendingIds = new Set([...pendingIds].filter((id) => id !== command.id))
    })
  if (command.op !== 'interrupt') commandChain = finished
}

function receiveLine($, line) {
  const parsed = parseCommand(line)
  if (parsed.kind === 'garbage') {
    $.ui.log('skipped a bad command line: ' + parsed.error)
    return
  }
  if (parsed.kind === 'invalid') {
    if (pendingIds.has(parsed.id)) return
    enqueueReport($, () => ackReport(parsed.id, false, parsed.error))
    return
  }
  enqueueCommand($, parsed.command)
}

// ---- the stream pump -----------------------------------------------------

// Resolves with why the stream ended, and whether the daemon refused this
// launch for good.
async function runStream($) {
  const argv = bridgeArgv()
  const stream = $.process.spawn({ argv })
  await enqueueReport($, () => sendHello($))
  let carry = ''
  let stderr = ''
  for await (const chunk of stream) {
    if (chunk.stream !== 'stdout') {
      stderr = (stderr + chunk.text).slice(-2000)
      continue
    }
    const split = splitLines(carry, chunk.text)
    carry = split.carry
    split.lines.forEach((line) => receiveLine($, line))
  }
  // A partial last line is dropped: the daemon redelivers anything unacked.
  const result = await stream.result
  return { why: describeExit(result, stderr), isStale: result?.code === STALE_LAUNCH_EXIT_CODE }
}

function streamEnded($, why) {
  if (isStreamFailing) return
  isStreamFailing = true
  $.ui.log('bridge stream ended' + (why ? ': ' + why : '') + '; reconnecting')
}

async function pump($) {
  let backoff = BACKOFF_INITIAL_MS
  for (;;) {
    const startedAt = await $.clock.now()
    let why = ''
    try {
      const ended = await runStream($)
      if (ended.isStale) return goDormant($)
      why = ended.why
    } catch (err) {
      why = errorText(err)
    }
    const lived = (await $.clock.now()) - startedAt
    if (lived > BACKOFF_RESET_AFTER_MS) isStreamFailing = false
    streamEnded($, why)
    const plan = nextBackoff(backoff, lived)
    await $.clock.sleep(plan.waitMs)
    backoff = plan.next
  }
}

function goDormant($) {
  isDormant = true
  $.ui.log('this claude is no longer ' + config.agent + "'s current leo launch; the bridge stays off until the mod reloads")
}

// ---- hooks ---------------------------------------------------------------

async function onSessionStart($) {
  if (isStarted) return
  isStarted = true
  config = await readConfig($)
  if (config === null) {
    $.ui.log('LEO_BRIDGE_BIN, LEO_BRIDGE_AGENT or LEO_BRIDGE_LAUNCH is unset; bridge disabled')
    return
  }
  touchEntry($)
  $.clock.after(0, () => pruneAcked($))
  $.clock.after(0, () => pump($))
}

export function register(on) {
  on('session.start', async ($, e, next) => {
    await onSessionStart($)
    return next(e)
  })

  on('turn.start', async ($, e, next) => {
    // Defensive: per the v2.1.289 typings only the main loop raises
    // turn.start, but a subagent's must never pass for the main loop's.
    if (e.agentId) return next(e)
    markRunning(e.turnId)
    if (isBridging()) {
      enqueueReport($, () => helloIfSessionChanged($))
      const fields = { event_id: eventId('turn.start', e.turnId), prompt: reportText(e.text) }
      enqueueReport($, () => eventReport('turn.start', defined(fields)))
    }
    return next(e)
  })

  on('turn.complete', async ($, e, next) => {
    if (e.agentId) return next(e)
    markIdle()
    if (isBridging()) {
      enqueueReport($, () => turnCompleteReport($, e))
      touchEntry($)
    }
    return next(e)
  })

  on('session.end', async ($, e, next) => {
    // A dormant process leaves the acked entry alone: a successor under the
    // same key may be using it.
    if (isBridging()) {
      // session.end hooks share a 1.5 s budget: wait briefly, then move on.
      const fields = { event_id: eventId('session.end', e.sessionId), reason: e.reason }
      const sent = enqueueReport($, () => eventReport('session.end', defined(fields)))
      const forgotten = forgetDispatchAcked($, e.reason)
      await Promise.race([Promise.all([sent, forgotten]), $.clock.sleep(SESSION_END_WAIT_MS)])
    }
    return next(e)
  })
}
