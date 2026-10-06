import { expect, test } from 'claude-code/testing'
import { ACKED_MAX_AGE_MS } from '../hooks/protocol.js'
import { advance, AGENT, BIN, Feed, LAUNCH, setup, start } from './harness.ts'

// The harness clock starts here.
const NOW = 1_000_000
const DAY = 24 * 60 * 60 * 1000
const deliver = (id: string, text: string) => ({ id, op: 'deliver', text, as_user: false })
const DISPATCH = 'dispatch.d-1'
const DISPATCH_ENV = { LEO_BRIDGE_BIN: BIN, LEO_BRIDGE_AGENT: DISPATCH, LEO_BRIDGE_LAUNCH: LAUNCH }

test('acked ids are stored with when they were last written', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed] })
  await start($, h)
  feed.line(deliver('c1', 'x'))
  await h.settle()
  expect(h.store.get('acked:' + AGENT)).toMatchObject({ ids: ['c1'], at: NOW })
})

test('an earlier process\'s entry is honoured', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed], store: { ['acked:' + AGENT]: { ids: ['old'], at: NOW - DAY } } })
  await start($, h)
  feed.line(deliver('old', 'again'))
  await h.settle()
  expect(h.submits).toEqual([])
})

test('session.start prunes other keys\' entries left untouched for seven days', async ($, on) => {
  expect(ACKED_MAX_AGE_MS).toBe(7 * DAY)
  const h = setup(on, {
    store: {
      'acked:stale': { ids: ['a'], at: NOW - 8 * DAY },
      'acked:fresh': { ids: ['b'], at: NOW - DAY },
      // An entry without a time cannot be dated; it goes.
      'acked:undated': ['c'],
      // This process's own entry is in use, however old.
      ['acked:' + AGENT]: { ids: ['own'], at: NOW - 30 * DAY },
      'not-ours': 'kept',
    },
  })
  await start($, h)
  await h.settle()
  expect([...h.store.keys()].sort()).toEqual(['acked:fresh', 'acked:' + AGENT, 'not-ours'].sort())
})

// A live agent can sit idle for days: its entry is restamped as it starts
// and after every turn, so only an entry untouched for seven days (a
// process long gone) is ever pruned.
test('session.start restamps this process\'s own entry', async ($, on) => {
  const h = setup(on, { store: { ['acked:' + AGENT]: { ids: ['own'], at: NOW - 6 * DAY } } })
  await start($, h)
  await h.settle()
  expect(h.store.get('acked:' + AGENT)).toEqual({ ids: ['own'], at: NOW })
})

test('every main-loop turn.complete restamps the entry', { timeoutMs: 10_000 }, async ($, on) => {
  const h = setup(on)
  await start($, h)
  await advance(h, DAY)
  await $.turn.start({ text: 'x', turnId: 't1' })
  await $.turn.complete({ turnId: 't1', answer: '', durationMs: 1, isAborted: false, reason: 'answer' })
  await h.settle()
  expect(h.store.get('acked:' + AGENT)).toEqual({ ids: [], at: NOW + DAY })
})

test('a dispatch\'s entry is deleted when its session finally ends', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed], env: DISPATCH_ENV })
  await start($, h)
  feed.line(deliver('c1', 'x'))
  await h.settle()
  expect(h.store.has('acked:' + DISPATCH)).toBe(true)
  await $.session.end({ reason: 'prompt_input_exit', sessionId: 'sess-1', resume: {} as any })
  await h.settle()
  expect(h.store.has('acked:' + DISPATCH)).toBe(false)
})

test('a dispatch\'s told delegation goes with it, every session\'s', async ($, on) => {
  const told = (session: string) => ({ session, enabled: false, section: '', at: 1_000_000 })
  const store = { ['told:' + DISPATCH + ':sess-0']: told('sess-0'), ['told:' + DISPATCH + ':sess-1']: told('sess-1'), ['told:worker:sess-1']: told('sess-1') }
  const h = setup(on, { feeds: [new Feed()], env: DISPATCH_ENV, store })
  await start($, h)
  await $.session.end({ reason: 'prompt_input_exit', sessionId: 'sess-1', resume: {} as any })
  await h.settle()
  expect([...h.store.keys()].filter((k) => k.startsWith('told:'))).toEqual(['told:worker:sess-1'])
})

test('a dispatch keeps its entry across /clear and a resume', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed], env: DISPATCH_ENV })
  await start($, h)
  feed.line(deliver('c1', 'x'))
  await h.settle()
  for (const reason of ['clear', 'resume'] as const) {
    await $.session.end({ reason, sessionId: 'sess-1', resume: {} as any })
    await h.settle()
    expect(h.store.has('acked:' + DISPATCH)).toBe(true)
  }
})

test('an agent keeps its entry when its session ends: it resumes later', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed] })
  await start($, h)
  feed.line(deliver('c1', 'x'))
  await h.settle()
  await $.session.end({ reason: 'other', sessionId: 'sess-1', resume: {} as any })
  await h.settle()
  expect(h.store.has('acked:' + AGENT)).toBe(true)
})

// The restamp after a dispatch's last turn may still be landing when its
// session ends: it must not bring the deleted entry back.
test('a dispatch\'s entry stays deleted behind a restamp still landing', async ($, on) => {
  let hold: Promise<void> | null = null
  let release: (() => void) | null = null
  const h = setup(on, { env: DISPATCH_ENV, beforeStoreSet: () => hold ?? undefined })
  await start($, h)
  await h.settle()
  hold = new Promise<void>((r) => (release = r))
  await $.turn.start({ text: 'x', turnId: 't1' })
  await $.turn.complete({ turnId: 't1', answer: 'done', durationMs: 1, isAborted: false, reason: 'answer' })
  await h.settle()
  const ended = $.session.end({ reason: 'prompt_input_exit', sessionId: 'sess-1', resume: {} as any })
  await h.settle()
  hold = null
  release!()
  await ended
  await h.settle()
  expect(h.store.has('acked:' + DISPATCH)).toBe(false)
})
