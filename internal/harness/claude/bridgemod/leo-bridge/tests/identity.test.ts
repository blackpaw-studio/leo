import { expect, test } from 'claude-code/testing'
import { acks, advance, events, Feed, setup, start } from './harness.ts'

const deliver = (id: string, text: string, asUser = false) => ({ id, op: 'deliver', text, as_user: asUser })

const starts = (h: ReturnType<typeof setup>) => events(h).filter((r) => r.name === 'turn.start')
const completes = (h: ReturnType<typeof setup>) => events(h).filter((r) => r.name === 'turn.complete')

// leo attributes a Stop to its turn by id, not by arrival order, so the mod
// says which turn each event belongs to and which leo command submitted it.
test('turn.start and turn.complete carry the engine turn id', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.turn.start({ text: 'typed by a person', turnId: 't1' })
  await $.turn.complete({ turnId: 't1', answer: 'done', durationMs: 5, isAborted: false, reason: 'answer' })
  await h.settle()
  expect(starts(h)).toEqual([{ type: 'event', name: 'turn.start', event_id: 'turn.start:t1', turn_id: 't1', prompt: 'typed by a person' }])
  expect(completes(h)[0]).toMatchObject({ event_id: 'turn.complete:t1', turn_id: 't1' })
})

test('a deliver stamps the turn it starts with its command id and origin', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, {
    feeds: [feed],
    submit: async (e) => {
      await $.turn.start({ text: String(e.text), turnId: 't7' })
      return { text: e.text }
    },
  })
  await start($, h)
  feed.line(deliver('c1', 'go', true))
  await h.settle()
  expect(starts(h)).toEqual([
    { type: 'event', name: 'turn.start', event_id: 'turn.start:t7', turn_id: 't7', prompt: 'go', command_id: 'c1', origin: 'plugin' },
  ])
  expect(acks(h)).toEqual([{ type: 'ack', id: 'c1', ok: true }])
})

test('a plugin-framed deliver is still matched to its turn', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, {
    feeds: [feed],
    submit: async (e) => {
      await $.turn.start({ text: 'The leo-bridge plugin sent a message:\n' + String(e.text), turnId: 't7' })
      return { text: e.text }
    },
  })
  await start($, h)
  feed.line(deliver('c1', 'From orch via leo:\n\nnext'))
  await h.settle()
  expect(starts(h)[0]).toMatchObject({ turn_id: 't7', command_id: 'c1', origin: 'plugin' })
})

// A turn nobody at leo asked for (a person typing, a wake) is unstamped.
test('a turn started without a deliver carries no command id', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.turn.start({ text: 'typed', turnId: 't1' })
  await h.settle()
  expect(starts(h)[0]).not.toHaveProperty('command_id')
  expect(starts(h)[0]).not.toHaveProperty('origin')
})

// $.prompt.submit waits for the running turn to end: the person's turn ends
// unstamped, and the deliver's own turn, started after it, carries the stamp.
test('a deliver queued behind a running turn stamps its own turn, not the running one', async ($, on) => {
  const feed = new Feed()
  let release: (() => void) | null = null
  const h = setup(on, {
    feeds: [feed],
    submit: async (e) => {
      await new Promise<void>((r) => (release = r))
      await $.turn.start({ text: String(e.text), turnId: 't2' })
      return { text: e.text }
    },
  })
  await start($, h)
  await $.turn.start({ text: 'typed', turnId: 't1' })
  feed.line(deliver('c1', 'follow up'))
  await h.settle()
  await $.turn.complete({ turnId: 't1', answer: 'first', durationMs: 5, isAborted: false, reason: 'answer' })
  release!()
  await h.settle()
  const order = events(h).map((r) => `${r.name}:${r.turn_id}:${r.command_id ?? '-'}`)
  expect(order).toEqual(['turn.start:t1:-', 'turn.complete:t1:-', 'turn.start:t2:c1'])
})

// Another turn can start while a deliver waits (a prompt the person queued
// earlier): it must not take the deliver's stamp.
test('a turn started by someone else while a deliver waits is not stamped', async ($, on) => {
  const feed = new Feed()
  let release: (() => void) | null = null
  const h = setup(on, {
    feeds: [feed],
    submit: async (e) => {
      await new Promise<void>((r) => (release = r))
      await $.turn.start({ text: String(e.text), turnId: 't3' })
      return { text: e.text }
    },
  })
  await start($, h)
  feed.line(deliver('c1', 'follow up'))
  await h.settle()
  await $.turn.start({ text: 'a queued prompt that ran first', turnId: 't2' })
  await $.turn.complete({ turnId: 't2', answer: 'x', durationMs: 5, isAborted: false, reason: 'answer' })
  release!()
  await h.settle()
  expect(starts(h).map((r) => [r.turn_id, r.command_id])).toEqual([
    ['t2', undefined],
    ['t3', 'c1'],
  ])
})

test('a dropped deliver leaves no stamp for a later turn with the same text', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed], submit: () => ({ drop: 'refused' }) })
  await start($, h)
  feed.line(deliver('c1', 'go'))
  await h.settle()
  await $.turn.start({ text: 'go', turnId: 't1' })
  await h.settle()
  expect(starts(h)[0]).not.toHaveProperty('command_id')
})

test('an aborted turn.complete still names its turn', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.turn.start({ text: 'work', turnId: 't1' })
  await $.turn.complete({ turnId: 't1', answer: 'partial', durationMs: 5, isAborted: true, reason: 'aborted' })
  await advance(h, 0)
  expect(completes(h)[0]).toMatchObject({ turn_id: 't1', reason: 'aborted' })
})
