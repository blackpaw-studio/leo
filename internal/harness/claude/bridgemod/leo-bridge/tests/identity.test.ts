import { expect, test } from 'claude-code/testing'
import { acks, advance, events, Feed, flush, setup, start } from './harness.ts'

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

// A person (or a wake) submits through the same prompt.submit chain the mod's
// hook sees, then the engine starts their turn. The engine runs submits in
// the order they were made, once the session is idle: the tests below gate
// each submit's turn so the order is the test's to choose.
const isDeliver = (e: { origin?: unknown }) => (e.origin as { name?: string } | undefined)?.name === 'leo-bridge'

// What a turn.start looks like to leo: [turn id, command id].
const stamps = (h: ReturnType<typeof setup>) => starts(h).map((r) => [r.turn_id, r.command_id])

// An engine whose person-typed submits and the mod's delivers each wait for a
// gate, then start the turn they carry.
function gatedEngine() {
  const gates = new Map<string, () => void>()
  const pending: string[] = []
  return {
    submit: ($: any) => async (e: any) => {
      const who = isDeliver(e) ? 'deliver' : 'person'
      const turnId = `t${pending.push(who) + 1}`
      await new Promise<void>((r) => gates.set(`${who}:${e.text}`, r))
      await $.turn.start({ text: String(e.text), turnId })
      await $.turn.complete({ turnId, answer: 'x', durationMs: 5, isAborted: false, reason: 'answer' })
      return { text: e.text }
    },
    run: async (who: string, text: string) => {
      gates.get(`${who}:${text}`)!()
      await flush()
    },
  }
}

// The deliver is the mod's own: a turn someone else's submit starts is not
// the deliver's, whichever words it carries.
test('a prompt a person queued before a deliver runs first and is not stamped', async ($, on) => {
  const feed = new Feed()
  const engine = gatedEngine()
  const h = setup(on, { feeds: [feed], submit: engine.submit($) })
  await start($, h)
  void $.prompt.submit({ text: 'typed while it ran', asUser: true })
  await flush()
  feed.line(deliver('c1', 'follow up'))
  await flush()
  await engine.run('person', 'typed while it ran')
  await engine.run('deliver', 'follow up')
  expect(stamps(h)).toEqual([
    ['t2', undefined],
    ['t3', 'c1'],
  ])
})

// The deliver's text is no evidence of its turn: a person's identical words
// are theirs. Which turn is whose is the order the submits were made in.
test('a person submitting a waiting deliver\'s exact text does not take its stamp', async ($, on) => {
  const feed = new Feed()
  const engine = gatedEngine()
  const h = setup(on, { feeds: [feed], submit: engine.submit($) })
  await start($, h)
  void $.prompt.submit({ text: 'go', asUser: true })
  await flush()
  feed.line(deliver('c1', 'go'))
  await flush()
  await engine.run('person', 'go')
  await engine.run('deliver', 'go')
  expect(stamps(h)).toEqual([
    ['t2', undefined],
    ['t3', 'c1'],
  ])
})

test('a deliver submitted before a person\'s identical text keeps its stamp', async ($, on) => {
  const feed = new Feed()
  const engine = gatedEngine()
  const h = setup(on, { feeds: [feed], submit: engine.submit($) })
  await start($, h)
  feed.line(deliver('c1', 'go'))
  await flush()
  void $.prompt.submit({ text: 'go', asUser: true })
  await flush()
  await engine.run('deliver', 'go')
  await engine.run('person', 'go')
  expect(stamps(h)).toEqual([
    ['t2', 'c1'],
    ['t3', undefined],
  ])
})

// A turn nothing here submitted (a continuation) is not the deliver's even
// though no submit was seen for it.
test('a turn started by the engine itself while a deliver waits is not stamped', async ($, on) => {
  const feed = new Feed()
  const engine = gatedEngine()
  const h = setup(on, { feeds: [feed], submit: engine.submit($) })
  await start($, h)
  feed.line(deliver('c1', 'follow up'))
  await flush()
  await $.turn.start({ text: '', turnId: 'tc' })
  await $.turn.complete({ turnId: 'tc', answer: 'x', durationMs: 5, isAborted: false, reason: 'answer' })
  await engine.run('deliver', 'follow up')
  expect(stamps(h)).toEqual([
    ['tc', undefined],
    ['t2', 'c1'],
  ])
})

// A prompt typed over a running turn can fold into it and never start a
// turn of its own: it must not hide the next deliver's turn.
test('a submit that never started a turn does not hide a later deliver\'s stamp', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, {
    feeds: [feed],
    submit: async (e) => {
      if (isDeliver(e)) await $.turn.start({ text: String(e.text), turnId: 't2' })
      return { text: e.text }
    },
  })
  await start($, h)
  await $.prompt.submit({ text: 'folded into the running turn', asUser: true })
  feed.line(deliver('c1', 'next'))
  await h.settle()
  expect(stamps(h)).toEqual([['t2', 'c1']])
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
