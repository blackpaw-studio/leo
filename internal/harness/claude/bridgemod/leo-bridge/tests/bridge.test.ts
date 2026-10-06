import { expect, test } from 'claude-code/testing'
import { STALE_LAUNCH_EXIT_CODE } from '../hooks/protocol.js'
import { acks, advance, AGENT, BIN, events, Feed, LAUNCH, REPORT_ARGV, setup, start, STREAM_ARGV } from './harness.ts'

const deliver = (id: string, text: string, asUser = false) => ({ id, op: 'deliver', text, as_user: asUser })

test('missing env: no stream, no reports, logs once', async ($, on) => {
  const h = setup(on, { env: { LEO_BRIDGE_AGENT: AGENT } })
  await start($, h)
  await $.turn.start({ text: 'hi', turnId: 't1' })
  await $.turn.complete({ turnId: 't1', answer: '', durationMs: 1, isAborted: false, reason: 'answer' })
  await h.settle()
  expect(h.spawns).toEqual([])
  expect(h.reports).toEqual([])
  expect(h.logs.length).toBe(1)
  expect(h.logs[0]).toContain('LEO_BRIDGE_BIN')
})

// leo mints a launch token per claude launch; the daemon refuses a stream or
// report without the key's current one, so without it there is no bridge.
test('missing launch: the bridge is disabled', async ($, on) => {
  const h = setup(on, { env: { LEO_BRIDGE_BIN: BIN, LEO_BRIDGE_AGENT: AGENT } })
  await start($, h)
  await $.turn.start({ text: 'hi', turnId: 't1' })
  await h.settle()
  expect(h.spawns).toEqual([])
  expect(h.reports).toEqual([])
  expect(h.logs.length).toBe(1)
  expect(h.logs[0]).toContain('LEO_BRIDGE_LAUNCH')
})

test('spawns the bridge stream and says hello before acks', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed] })
  await start($, h)
  expect(h.spawns).toEqual([STREAM_ARGV])
  // A fresh module cannot tell whether a turn runs, so it leaves busy out.
  expect(h.reports[0]).toEqual({ type: 'hello', session_id: 'sess-1', claude_version: '2.1.289' })
  expect(h.reportArgv[0]).toEqual(REPORT_ARGV)
  feed.line(deliver('c1', 'hello'))
  await h.settle()
  expect(h.reports.map((r) => r.type)).toEqual(['hello', 'ack'])
})

test('reassembles a command split across chunks', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed] })
  await start($, h)
  const line = JSON.stringify(deliver('c1', 'split text', true))
  feed.push(line.slice(0, 10))
  await h.settle()
  expect(h.submits).toEqual([])
  feed.push(line.slice(10) + '\n')
  await h.settle()
  expect(h.submits.length).toBe(1)
  // The engine folds the plugin's asUser flag into the event's origin.
  expect(h.submits[0]).toMatchObject({ text: 'split text', origin: { kind: 'plugin', asUser: true } })
  expect(acks(h)).toEqual([{ type: 'ack', id: 'c1', ok: true }])
})

test('as_user false submits plugin-framed text', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed] })
  await start($, h)
  feed.line(deliver('c1', 'from peer', false))
  await h.settle()
  expect(h.submits[0]!.text).toBe('from peer')
  expect(h.submits[0]).toMatchObject({ origin: { kind: 'plugin', name: 'leo-bridge' } })
  expect((h.submits[0]!.origin as Record<string, unknown>).asUser).toBeFalsy()
})

test('processes commands in order, one at a time', async ($, on) => {
  const feed = new Feed()
  let release: (() => void) | null = null
  const h = setup(on, {
    feeds: [feed],
    submit: async (e) => {
      if (e.text === 'first') await new Promise<void>((r) => (release = r))
      return { text: e.text }
    },
  })
  await start($, h)
  feed.push(JSON.stringify(deliver('a', 'first')) + '\n' + JSON.stringify(deliver('b', 'second')) + '\n')
  await h.settle()
  expect(h.submits.map((s) => s.text)).toEqual(['first'])
  expect(acks(h)).toEqual([])
  release!()
  await h.settle()
  expect(h.submits.map((s) => s.text)).toEqual(['first', 'second'])
  expect(acks(h).map((a) => a.id)).toEqual(['a', 'b'])
})

test('a dropped deliver acks ok:false with the reason', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed], submit: () => ({ drop: 'policy says no' }) })
  await start($, h)
  feed.line(deliver('c1', 'x'))
  await h.settle()
  expect(acks(h)).toEqual([{ type: 'ack', id: 'c1', ok: false, error: 'policy says no' }])
})

test('a rejected deliver acks ok:false', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, {
    feeds: [feed],
    submit: () => {
      throw new Error('boom')
    },
  })
  await start($, h)
  feed.line(deliver('c1', 'x'))
  await h.settle()
  const [ack] = acks(h)
  expect(ack).toMatchObject({ type: 'ack', id: 'c1', ok: false })
  expect(typeof ack!.error).toBe('string')
})

test('dedup: a repeated id is re-acked without executing', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed] })
  await start($, h)
  feed.line(deliver('c1', 'once'))
  await h.settle()
  feed.line(deliver('c1', 'once'))
  await h.settle()
  expect(h.submits.length).toBe(1)
  expect(acks(h)).toEqual([
    { type: 'ack', id: 'c1', ok: true },
    { type: 'ack', id: 'c1', ok: true },
  ])
  expect(h.store.get('acked:' + AGENT)).toMatchObject({ ids: ['c1'] })
})

test('dedup: ids acked by an earlier process are honoured from the store', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed], store: { ['acked:' + AGENT]: ['old'] } })
  await start($, h)
  feed.line(deliver('old', 'again'))
  await h.settle()
  expect(h.submits).toEqual([])
  expect(acks(h)).toEqual([{ type: 'ack', id: 'old', ok: true }])
})

test('dedup: the acked list keeps only the last 500 ids', async ($, on) => {
  const seeded = Array.from({ length: 500 }, (_, i) => 'id' + i)
  const feed = new Feed()
  const h = setup(on, { feeds: [feed], store: { ['acked:' + AGENT]: seeded } })
  await start($, h)
  feed.line(deliver('new', 'x'))
  await h.settle()
  const list = (h.store.get('acked:' + AGENT) as { ids: string[] }).ids
  expect(list.length).toBe(500)
  expect(list[0]).toBe('id1')
  expect(list[499]).toBe('new')
})

test('dedup: a failed command is not remembered, so redelivery retries it', async ($, on) => {
  const feed = new Feed()
  let calls = 0
  const h = setup(on, {
    feeds: [feed],
    submit: (e) => (++calls === 1 ? { drop: 'busy' } : { text: e.text }),
  })
  await start($, h)
  feed.line(deliver('c1', 'x'))
  await h.settle()
  feed.line(deliver('c1', 'x'))
  await h.settle()
  expect(h.submits.length).toBe(2)
  expect(acks(h).map((a) => a.ok)).toEqual([false, true])
})

test('compact waits for the running turn to finish', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed] })
  await start($, h)
  await $.turn.start({ text: 'work', turnId: 't1' })
  feed.line({ id: 'k1', op: 'compact', instructions: 'keep the plan' })
  await h.settle()
  expect(h.compacts).toEqual([])
  await $.turn.complete({ turnId: 't1', answer: '', durationMs: 5, isAborted: false, reason: 'answer' })
  await h.settle()
  expect(h.compacts.length).toBe(1)
  expect(h.compacts[0]).toMatchObject({ instructions: 'keep the plan' })
  expect(acks(h)).toEqual([{ type: 'ack', id: 'k1', ok: true }])
})

test('compact while idle runs at once; a skip acks ok:false', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed], compact: () => ({ skip: 'nothing to compact' }) })
  await start($, h)
  feed.line({ id: 'k1', op: 'compact' })
  await h.settle()
  expect(h.compacts.length).toBe(1)
  expect(h.compacts[0]!.instructions).toBeUndefined()
  expect(acks(h)).toEqual([{ type: 'ack', id: 'k1', ok: false, error: 'nothing to compact' }])
})

test('a subagent turn.complete does not end the main turn', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed] })
  await start($, h)
  await $.turn.start({ text: 'work', turnId: 't1' })
  feed.line({ id: 'k1', op: 'compact' })
  await $.turn.complete({ turnId: 's1', agentId: 'sub', answer: '', durationMs: 5, isAborted: false, reason: 'answer' })
  await h.settle()
  expect(h.compacts).toEqual([])
  expect(events(h).map((e) => e.name)).toEqual(['turn.start'])
})

test('clear runs the built-in /clear once the running turn ends', async ($, on) => {
  const feed = new Feed()
  // The built-in /clear ends the session; the process goes on under a new one.
  const h = setup(on, {
    feeds: [feed],
    command: () => {
      h.sessionId = 'sess-2'
      return {}
    },
  })
  await start($, h)
  await $.turn.start({ text: 'work', turnId: 't1' })
  feed.line({ id: 'x1', op: 'clear' })
  await h.settle()
  expect(h.commands).toEqual([])
  await $.turn.complete({ turnId: 't1', answer: '', durationMs: 5, isAborted: false, reason: 'answer' })
  await h.settle()
  expect(h.commands).toEqual([expect.objectContaining({ command: 'clear', args: '' })])
  expect(acks(h)).toEqual([{ type: 'ack', id: 'x1', ok: true }])
})

test('interrupt aborts the running turn by id', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed] })
  await start($, h)
  await $.turn.start({ text: 'work', turnId: 't9' })
  feed.line({ id: 'i1', op: 'interrupt' })
  await h.settle()
  expect(h.aborts).toEqual([{ turnId: 't9' }])
  expect(acks(h)).toEqual([{ type: 'ack', id: 'i1', ok: true }])
})

test('interrupt jumps ahead of a deliver that is waiting for idle', async ($, on) => {
  const feed = new Feed()
  let release: (() => void) | null = null
  const h = setup(on, {
    feeds: [feed],
    // $.prompt.submit waits for the running turn to end before it resolves.
    submit: async (e) => {
      await new Promise<void>((r) => (release = r))
      return { text: e.text }
    },
  })
  await start($, h)
  await $.turn.start({ text: 'work', turnId: 't1' })
  feed.line(deliver('d1', 'queued behind the turn'))
  feed.line({ id: 'k1', op: 'compact' })
  feed.line({ id: 'i1', op: 'interrupt' })
  await h.settle()
  expect(h.aborts).toEqual([{ turnId: 't1' }])
  expect(acks(h)).toEqual([{ type: 'ack', id: 'i1', ok: true }])
  // The serialized commands still run afterwards, in order.
  await $.turn.complete({ turnId: 't1', answer: '', durationMs: 5, isAborted: true, reason: 'aborted' })
  release!()
  await h.settle()
  expect(acks(h).map((a) => a.id)).toEqual(['i1', 'd1', 'k1'])
})

test('a repeated interrupt id is re-acked without aborting again', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed] })
  await start($, h)
  await $.turn.start({ text: 'work', turnId: 't1' })
  feed.line({ id: 'i1', op: 'interrupt' })
  await h.settle()
  feed.line({ id: 'i1', op: 'interrupt' })
  await h.settle()
  expect(h.aborts.length).toBe(1)
  expect(acks(h)).toEqual([
    { type: 'ack', id: 'i1', ok: true },
    { type: 'ack', id: 'i1', ok: true },
  ])
})

test('hello is re-sent when the session id changes (e.g. after /clear)', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.turn.start({ text: 'a', turnId: 't1' })
  await h.settle()
  expect(h.reports.filter((r) => r.type === 'hello').length).toBe(1)
  h.sessionId = 'sess-2'
  await $.turn.start({ text: 'b', turnId: 't2' })
  await h.settle()
  const types = h.reports.map((r) => (r.type === 'event' ? r.name : r.type))
  expect(types).toEqual(['hello', 'turn.start', 'hello', 'turn.start'])
  // Re-said from inside turn.start, so the turn is already running.
  expect(h.reports[2]).toEqual({ type: 'hello', session_id: 'sess-2', claude_version: '2.1.289', busy: true })
})

test('interrupt while idle acks ok without aborting', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed] })
  await start($, h)
  feed.line({ id: 'i1', op: 'interrupt' })
  await h.settle()
  expect(h.aborts).toEqual([])
  expect(acks(h)).toEqual([{ type: 'ack', id: 'i1', ok: true }])
})

test('unknown ops ack ok:false; malformed lines are skipped', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed] })
  await start($, h)
  feed.push('not json\n\n{"op":"deliver","text":"no id"}\n')
  feed.line({ id: 'u1', op: 'teleport' })
  feed.line({ id: 'd1', op: 'deliver' })
  await h.settle()
  expect(h.submits).toEqual([])
  expect(acks(h)).toEqual([
    { type: 'ack', id: 'u1', ok: false, error: 'unknown op: teleport' },
    { type: 'ack', id: 'd1', ok: false, error: 'deliver: text must be a string' },
  ])
})

test('turn events are reported; turn.complete carries usage', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.turn.start({ text: 'go', turnId: 't1' })
  await $.turn.complete({ turnId: 't1', answer: 'done', durationMs: 5, isAborted: false, reason: 'answer' })
  await h.settle()
  expect(events(h)).toEqual([
    { type: 'event', name: 'turn.start', event_id: 'turn.start:t1', prompt: 'go' },
    {
      type: 'event',
      name: 'turn.complete',
      event_id: 'turn.complete:t1',
      message: 'done',
      usage: { startedAt: 1, context: { tokens: 10, window: 200000, percent: 0 }, rateLimits: [], cost: { usd: 0.01 } },
    },
  ])
})

test('session.end is reported with its reason', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.session.end({ reason: 'clear', sessionId: 'sess-1', resume: {} as any })
  await h.settle()
  expect(events(h)).toEqual([{ type: 'event', name: 'session.end', event_id: 'session.end:sess-1', reason: 'clear' }])
})

// Each mock-clock move costs ~1 s of real time in the kit, so the timing
// tests get a longer budget; the backoff arithmetic itself (doubling, the
// 5 s cap) is covered quickly by protocol.test.ts.
test('a dead stream respawns with doubling backoff and says hello again', { timeoutMs: 20_000 }, async ($, on) => {
  const feeds = [new Feed(), new Feed(), new Feed()]
  const h = setup(on, { feeds })
  await start($, h)
  expect(h.spawns.length).toBe(1)
  feeds[0]!.end()
  await advance(h, 999)
  expect(h.spawns.length).toBe(1)
  await advance(h, 1)
  expect(h.spawns.length).toBe(2)
  feeds[1]!.end()
  await advance(h, 1999)
  expect(h.spawns.length).toBe(2)
  await advance(h, 1)
  expect(h.spawns.length).toBe(3)
  expect(h.reports.filter((r) => r.type === 'hello').length).toBe(3)
  // One log line per failure streak, naming how the child ended.
  expect(h.logs).toEqual(['bridge stream ended: exit 0; reconnecting'])
})

// `leo bridge` exits STALE_LAUNCH_EXIT_CODE when the daemon refuses this
// launch for good (a successor holds the key, or nobody adopts the
// session): the mod stops reconnecting and reporting until it reloads.
test('a launch the daemon refuses for good stops the bridge', { timeoutMs: 20_000 }, async ($, on) => {
  const feeds = [new Feed(), new Feed()]
  const h = setup(on, { feeds })
  await start($, h)
  const sent = h.attempts.length
  feeds[0]!.end(STALE_LAUNCH_EXIT_CODE)
  await advance(h, 10_000)
  expect(h.spawns.length).toBe(1)
  await $.turn.start({ text: 'hi', turnId: 't1' })
  await $.turn.complete({ turnId: 't1', answer: '', durationMs: 1, isAborted: false, reason: 'answer' })
  await h.settle()
  expect(h.attempts.length).toBe(sent)
  expect(h.logs.filter((l) => l.includes('no longer')).length).toBe(1)
})

// Any other failure (the daemon down, or not done adopting this session
// after a restart) is retried.
test('a stream that fails otherwise reconnects', { timeoutMs: 20_000 }, async ($, on) => {
  const feeds = [new Feed(), new Feed()]
  const h = setup(on, { feeds })
  await start($, h)
  feeds[0]!.end(1)
  await advance(h, 1000)
  expect(h.spawns.length).toBe(2)
})

test('backoff resets after a stream that lived over 60s', { timeoutMs: 20_000 }, async ($, on) => {
  const feeds = [new Feed(), new Feed(), new Feed(), new Feed()]
  const h = setup(on, { feeds })
  await start($, h)
  // Two quick deaths push the backoff to 4 s.
  feeds[0]!.end()
  await advance(h, 1000)
  feeds[1]!.end()
  await advance(h, 2000)
  expect(h.spawns.length).toBe(3)
  // The third stream lives 61 s, so the next wait is back to 1 s, not 4 s.
  await advance(h, 61_000)
  feeds[2]!.end()
  await advance(h, 1000)
  expect(h.spawns.length).toBe(4)
})

test('a redelivered command still in flight is not run twice', { timeoutMs: 10_000 }, async ($, on) => {
  const feeds = [new Feed(), new Feed()]
  let release: (() => void) | null = null
  const h = setup(on, {
    feeds,
    submit: async (e) => {
      await new Promise<void>((r) => (release = r))
      return { text: e.text }
    },
  })
  await start($, h)
  feeds[0]!.line(deliver('c1', 'slow'))
  await h.settle()
  feeds[0]!.end()
  await advance(h, 1000)
  expect(h.spawns.length).toBe(2)
  feeds[1]!.line(deliver('c1', 'slow'))
  await h.settle()
  release!()
  await h.settle()
  expect(h.submits.length).toBe(1)
  expect(acks(h)).toEqual([{ type: 'ack', id: 'c1', ok: true }])
})
