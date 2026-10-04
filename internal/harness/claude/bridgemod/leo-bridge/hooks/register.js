// leo-bridge: runs inside Claude Code. It streams commands from the leo daemon
// (`leo bridge --agent <name>`, one JSON command per stdout line), executes
// them in order, and reports acks and turn events back via
// `leo bridge report --agent <name> <json>`. It never decides policy.
//
// Mods API rules this file follows: every `$.ns.method` call is spelled in
// full, event names are string literals, and `$` is only passed to top-level
// functions of this file. Module state resets on hot reload.

import {
  ackedFromStore,
  ackReport,
  appendAcked,
  BACKOFF_INITIAL_MS,
  BACKOFF_RESET_AFTER_MS,
  describeExit,
  errorText,
  eventReport,
  helloReport,
  nextBackoff,
  parseCommand,
  REPORT_RETRY_DELAYS_MS,
  splitLines,
} from './protocol.js'

const REPORT_TIMEOUT_MS = 15_000
const SESSION_END_WAIT_MS = 1000
const COMPACT_ATTEMPTS = 5
const COMPACT_RETRY_MS = 250

// Bridge identity, read from the environment at session.start; null = disabled.
let config = null
let isStarted = false

// The main loop's running turn id, or null when idle, and who waits for idle.
let runningTurn = null
let idleWaiters = []

// Serial chains: reports keep per-process order; commands run one at a time.
let reportChain = Promise.resolve()
let commandChain = Promise.resolve()

// Ids queued or executing (guards redelivery while in flight) and the
// persisted list of ids already acked ok (null until loaded from $.store).
let pendingIds = new Set()
let ackedIds = null

// One log line per failure streak, so a down daemon doesn't flood the transcript.
let isStreamFailing = false
let isReportFailing = false

// The session id the last hello carried (null until the first hello).
let helloSessionId = null

function ackedKey() {
  return 'acked:' + config.agent
}

async function readConfig($) {
  const bin = await $.env.get('LEO_BRIDGE_BIN')
  const agent = await $.env.get('LEO_PROCESS_NAME')
  if (!bin || !agent) return null
  return { bin, agent }
}

// ---- reports -------------------------------------------------------------

// Sends one report, retrying with backoff while the daemon does not take it
// (down, restarting). Retrying is safe — acks are idempotent and events are
// state, not counters — and it happens inside the report chain, so later
// reports wait behind it and per-process order holds.
async function sendReport($, report) {
  const argv = [config.bin, 'bridge', 'report', '--agent', config.agent, JSON.stringify(report)]
  for (let attempt = 0; ; attempt++) {
    const why = await tryReport($, argv)
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
async function tryReport($, argv) {
  try {
    const result = await $.process.run(argv, { timeoutMs: REPORT_TIMEOUT_MS })
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

// Usage is a nice-to-have: a turn.complete without it still ends the turn
// in the daemon, which a lost turn.complete would leave stuck busy.
async function turnCompleteReport($) {
  try {
    const usage = await $.session.usage()
    return eventReport('turn.complete', { usage })
  } catch (err) {
    $.ui.log('reading session usage failed: ' + errorText(err))
    return eventReport('turn.complete')
  }
}

async function sendHello($) {
  const sessionId = await $.session.id()
  const version = await $.session.version()
  helloSessionId = sessionId
  return helloReport(sessionId, version.version, runningTurn !== null)
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
}

function markIdle() {
  runningTurn = null
  const waiters = idleWaiters
  idleWaiters = []
  waiters.forEach((resolve) => resolve())
}

function waitForIdle() {
  if (runningTurn === null) return Promise.resolve()
  return new Promise((resolve) => {
    idleWaiters = [...idleWaiters, resolve]
  })
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

async function rememberAcked($, id) {
  const next = appendAcked(ackedIds ?? [], id)
  ackedIds = next
  try {
    await $.store.set(ackedKey(), next)
  } catch (err) {
    $.ui.log('saving acked ids failed: ' + errorText(err))
  }
}

async function runDeliver($, command) {
  const args = command.asUser ? { text: command.text, asUser: true } : { text: command.text }
  const result = await $.prompt.submit(args)
  if (result && typeof result.drop === 'string') return { ok: false, error: result.drop }
  return { ok: true }
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
      // compact rejects while a turn runs; a turn may have started between
      // our idle check and the call (e.g. a just-delivered prompt).
      lastError = err
      if (runningTurn === null) await $.clock.sleep(COMPACT_RETRY_MS)
    }
  }
  return { ok: false, error: errorText(lastError) }
}

async function runClear($) {
  // Verified live on v2.1.289: a mod's command.run reaches the built-in
  // /clear (context wiped, session.end reason 'clear', the pump survives).
  await waitForIdle()
  await $.command.run({ command: 'clear', args: '' })
  return { ok: true }
}

async function runInterrupt($) {
  const turnId = runningTurn
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
    if (command.op === 'clear') return await runClear($)
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
  const result = await execute($, command)
  if (result.ok) await rememberAcked($, command.id)
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

async function runStream($) {
  const argv = [config.bin, 'bridge', '--agent', config.agent]
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
  return describeExit(await stream.result, stderr)
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
      why = await runStream($)
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

// ---- hooks ---------------------------------------------------------------

async function onSessionStart($) {
  if (isStarted) return
  isStarted = true
  config = await readConfig($)
  if (config === null) {
    $.ui.log('LEO_BRIDGE_BIN or LEO_PROCESS_NAME is unset; bridge disabled')
    return
  }
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
    if (config !== null) {
      enqueueReport($, () => helloIfSessionChanged($))
      enqueueReport($, () => eventReport('turn.start'))
    }
    return next(e)
  })

  on('turn.complete', async ($, e, next) => {
    if (e.agentId) return next(e)
    markIdle()
    if (config !== null) enqueueReport($, () => turnCompleteReport($))
    return next(e)
  })

  on('session.end', async ($, e, next) => {
    if (config !== null) {
      // session.end hooks share a 1.5 s budget: wait briefly, then move on.
      const sent = enqueueReport($, () => eventReport('session.end', { reason: e.reason }))
      await Promise.race([sent, $.clock.sleep(SESSION_END_WAIT_MS)])
    }
    return next(e)
  })
}
