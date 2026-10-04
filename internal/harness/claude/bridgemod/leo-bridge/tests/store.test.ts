import { expect, test } from 'claude-code/testing'
import { ACKED_MAX_AGE_MS } from '../hooks/protocol.js'
import { AGENT, BIN, Feed, setup, start } from './harness.ts'

// The harness clock starts here.
const NOW = 1_000_000
const DAY = 24 * 60 * 60 * 1000
const deliver = (id: string, text: string) => ({ id, op: 'deliver', text, as_user: false })
const DISPATCH = 'dispatch.d-1'

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

test('a dispatch\'s entry is deleted when its session finally ends', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed], env: { LEO_BRIDGE_BIN: BIN, LEO_BRIDGE_AGENT: DISPATCH } })
  await start($, h)
  feed.line(deliver('c1', 'x'))
  await h.settle()
  expect(h.store.has('acked:' + DISPATCH)).toBe(true)
  await $.session.end({ reason: 'prompt_input_exit', sessionId: 'sess-1', resume: {} as any })
  await h.settle()
  expect(h.store.has('acked:' + DISPATCH)).toBe(false)
})

test('a dispatch keeps its entry across /clear and a resume', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed], env: { LEO_BRIDGE_BIN: BIN, LEO_BRIDGE_AGENT: DISPATCH } })
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
