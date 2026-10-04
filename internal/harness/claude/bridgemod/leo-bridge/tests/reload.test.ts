// What the mod must get right across a hot reload of its module (module
// state resets; the engine, its queued prompts and the process live on) and
// in the gaps between a command and the turn it starts.
import { expect, test } from 'claude-code/testing'
import { acks, advance, AGENT, BIN, Feed, LAUNCH, setup, start } from './harness.ts'

const NOW = 1_000_000
const deliver = (id: string, text: string) => ({ id, op: 'deliver', text, as_user: false })
const complete = (turnId: string) => ({ turnId, answer: '', durationMs: 5, isAborted: false, reason: 'answer' as const })
const hellos = (h: { reports: Array<Record<string, unknown>> }) => h.reports.filter((r) => r.type === 'hello')

// A freshly loaded module cannot tell a new process from a reload in the
// middle of a turn, so it leaves busy to the daemon until it sees a turn.
test('hello leaves busy unsaid until the mod has seen a turn', { timeoutMs: 15_000 }, async ($, on) => {
  const feeds = [new Feed(), new Feed(), new Feed()]
  const h = setup(on, { feeds })
  await start($, h)
  expect(hellos(h)[0]).toEqual({ type: 'hello', session_id: 'sess-1', claude_version: '2.1.289' })
  await $.turn.start({ text: 'work', turnId: 't1' })
  feeds[0]!.end()
  await advance(h, 1000)
  expect(hellos(h)[1]).toMatchObject({ busy: true })
  await $.turn.complete(complete('t1'))
  feeds[1]!.end()
  await advance(h, 2000)
  expect(hellos(h)[2]).toMatchObject({ busy: false })
})

// After a reload mid-turn the mod has not seen the running turn start;
// compact refuses until it ends, so the retry waits for that turn.complete
// rather than burning its attempts in about a second.
test('a compact refused under a turn the mod never saw waits for that turn to end', { timeoutMs: 20_000 }, async ($, on) => {
  const feed = new Feed()
  let isTurnRunning = true
  const h = setup(on, {
    feeds: [feed],
    compact: () => {
      if (isTurnRunning) throw new Error('a turn is running')
      return { messages: [{ role: 'user', text: 'summary', toolUses: [] }] }
    },
  })
  await start($, h)
  feed.line({ id: 'k1', op: 'compact' })
  await h.settle()
  for (let i = 0; i < 4; i++) await advance(h, 300)
  expect(h.compacts.length).toBe(1)
  expect(acks(h)).toEqual([])
  isTurnRunning = false
  await $.turn.complete(complete('t0'))
  await h.settle()
  expect(h.compacts.length).toBe(2)
  expect(acks(h)).toEqual([{ type: 'ack', id: 'k1', ok: true }])
})

test('a clear that leaves the session as it was acks ok:false', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed], command: () => ({ text: 'clear is disabled here' }) })
  await start($, h)
  feed.line({ id: 'x1', op: 'clear' })
  await h.settle()
  expect(h.commands.length).toBe(1)
  const [ack] = acks(h)
  expect(ack).toMatchObject({ type: 'ack', id: 'x1', ok: false })
  expect(String(ack!.error)).toContain('clear is disabled here')
})

// $.prompt.submit resolves once its turn starts; an interrupt that lands
// before then has no turn id to abort yet, so it aborts the turn the submit
// starts.
test('an interrupt between a submit and its turn aborts that turn once it starts', async ($, on) => {
  const feed = new Feed()
  let release: (() => void) | null = null
  const h = setup(on, {
    feeds: [feed],
    submit: async (e) => {
      await new Promise<void>((r) => (release = r))
      return { text: e.text }
    },
  })
  await start($, h)
  feed.line(deliver('d1', 'go'))
  await h.settle()
  feed.line({ id: 'i1', op: 'interrupt' })
  await h.settle()
  expect(h.aborts).toEqual([])
  expect(acks(h)).toEqual([])
  await $.turn.start({ text: 'go', turnId: 't5' })
  await h.settle()
  expect(h.aborts).toEqual([{ turnId: 't5' }])
  expect(acks(h)).toEqual([{ type: 'ack', id: 'i1', ok: true }])
  release!()
  await h.settle()
  expect(acks(h).map((a) => a.id)).toEqual(['i1', 'd1'])
})

test('an interrupt waiting on a submit acks once the submit is dropped', async ($, on) => {
  const feed = new Feed()
  let release: (() => void) | null = null
  const h = setup(on, {
    feeds: [feed],
    submit: async () => {
      await new Promise<void>((r) => (release = r))
      return { drop: 'refused' }
    },
  })
  await start($, h)
  feed.line(deliver('d1', 'go'))
  await h.settle()
  feed.line({ id: 'i1', op: 'interrupt' })
  await h.settle()
  expect(acks(h)).toEqual([])
  release!()
  await h.settle()
  expect(h.aborts).toEqual([])
  expect(acks(h)).toEqual([
    { type: 'ack', id: 'd1', ok: false, error: 'refused' },
    { type: 'ack', id: 'i1', ok: true },
  ])
})

// The engine keeps a prompt the mod submitted across a reload of the mod,
// and runs it; the reloaded mod, handed the same command again by the
// daemon, must not submit it a second time.
test('a deliver an earlier load handed to the engine is not submitted again', async ($, on) => {
  const feed = new Feed()
  const entry = { ids: [], at: NOW, inflight: { launch: LAUNCH, ids: ['d1'] } }
  const h = setup(on, { feeds: [feed], store: { ['acked:' + AGENT]: entry } })
  await start($, h)
  feed.line(deliver('d1', 'go'))
  await h.settle()
  expect(h.submits).toEqual([])
  expect(acks(h)).toEqual([{ type: 'ack', id: 'd1', ok: true }])
})

// A new process starts with an empty prompt queue: what a dead process had
// handed its engine is gone and must be submitted again.
test('a deliver another launch handed to its engine is submitted again', async ($, on) => {
  const feed = new Feed()
  const entry = { ids: [], at: NOW, inflight: { launch: 'launch-0', ids: ['d1'] } }
  const h = setup(on, { feeds: [feed], store: { ['acked:' + AGENT]: entry } })
  await start($, h)
  feed.line(deliver('d1', 'go'))
  await h.settle()
  expect(h.submits.length).toBe(1)
  expect(acks(h)).toEqual([{ type: 'ack', id: 'd1', ok: true }])
})

test('a deliver is recorded in flight while the engine holds it', async ($, on) => {
  const feed = new Feed()
  let release: (() => void) | null = null
  const h = setup(on, {
    feeds: [feed],
    submit: async (e) => {
      await new Promise<void>((r) => (release = r))
      return { text: e.text }
    },
  })
  await start($, h)
  feed.line(deliver('d1', 'go'))
  await h.settle()
  expect(h.store.get('acked:' + AGENT)).toMatchObject({ inflight: { launch: LAUNCH, ids: ['d1'] } })
  release!()
  await h.settle()
  expect(h.store.get('acked:' + AGENT)).toMatchObject({ ids: ['d1'], inflight: { launch: LAUNCH, ids: [] } })
})

test('without a launch id nothing counts as handed off', async ($, on) => {
  const feed = new Feed()
  const entry = { ids: [], at: NOW, inflight: { launch: LAUNCH, ids: ['d1'] } }
  const h = setup(on, {
    feeds: [feed],
    env: { LEO_BRIDGE_BIN: BIN, LEO_BRIDGE_AGENT: AGENT },
    store: { ['acked:' + AGENT]: entry },
  })
  await start($, h)
  feed.line(deliver('d1', 'go'))
  await h.settle()
  expect(h.submits.length).toBe(1)
})

// An interrupt acks outside the command chain. Its store write must not
// land on top of a deliver's in-flight mark made meanwhile, or a reload
// would find the deliver unmarked and run it twice.
test('store writes from an interrupt and a deliver never undo each other', async ($, on) => {
  const feed = new Feed()
  let hold: Promise<void> | null = null
  let release: (() => void) | null = null
  const h = setup(on, {
    feeds: [feed],
    submit: () => new Promise(() => {}), // the deliver stays in flight
    beforeStoreSet: () => hold ?? undefined,
  })
  await start($, h)
  await $.turn.start({ text: 'work', turnId: 't1' })
  hold = new Promise<void>((r) => (release = r))
  feed.line({ id: 'i1', op: 'interrupt' })
  await h.settle()
  hold = null
  feed.line(deliver('d1', 'go'))
  await h.settle()
  release!()
  await h.settle()
  expect(h.store.get('acked:' + AGENT)).toMatchObject({ ids: ['i1'], inflight: { launch: LAUNCH, ids: ['d1'] } })
})
