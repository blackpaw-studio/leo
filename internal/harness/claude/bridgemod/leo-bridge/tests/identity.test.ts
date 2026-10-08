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

test('a deliver\'s turn is stamped whatever text the engine frames it with', async ($, on) => {
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

// What the engine does (live, claude 2.1.294): a person's or a wake's prompt
// raises prompt.submit with its origin, and at idle its turn.start follows at
// once. Typed over a running turn it carries that turn's id (turnId): either
// it enters the session at the turn's next step (folded: no turn.start of its
// own) or, with no step left, it runs as a turn of its own the moment the
// running one completes. The mod's own $.prompt.submit raises no
// prompt.submit hook for the mod.
const isDeliver = (e: { origin?: unknown }) => (e.origin as { name?: string } | undefined)?.name === 'leo-bridge'

const person = (text: string, turnId?: string, kind = 'composer') => ({ text, asUser: true, origin: { kind }, ...(turnId ? { turnId } : {}) }) as any

// [turn id, command id, origin] of every turn.start leo was told of.
const rows = (h: ReturnType<typeof setup>) => starts(h).map((r) => [r.turn_id, r.command_id, r.origin])

const complete = ($: any, turnId: string) => $.turn.complete({ turnId, answer: 'x', durationMs: 5, isAborted: false, reason: 'answer' })

// The engine's own step: an empty answer.
function answerSteps(on: any) {
  on('turn.step', async function* (_$: any, e: any) {
    return { turnId: e.turnId, index: e.index, answer: '', toolUses: [], stopReason: 'end_turn', usage: null }
  })
}

async function step($: any, turnId: string, index: number) {
  for await (const _ of $.turn.step({ turnId, index, model: 'claude-opus-5-5', messageCount: 1 })) {
    // drain
  }
}

// An engine whose delivers wait for a gate before their turn starts (the
// session is busy) and whose idle prompts start their turn at once.
function engine($: any) {
  const gates = new Map<string, () => void>()
  return {
    submit: async (e: any) => {
      if (isDeliver(e)) {
        await new Promise<void>((r) => gates.set(String(e.text), r))
        await $.turn.start({ text: String(e.text), turnId: `d:${e.text}` })
      } else if (e.text === 'refused') {
        return { drop: 'refused' }
      } else if (!e.turnId) {
        await $.turn.start({ text: String(e.text), turnId: `p:${e.text}` })
      }
      return { text: e.text }
    },
    run: async (text: string) => {
      gates.get(text)!()
      await flush()
    },
  }
}

// The deliver's text is no evidence of its turn: a person's identical words
// are theirs. The engine runs the prompts it holds in the order they were
// made, and the mod follows that order.
test('a prompt a person typed over a running turn before a deliver runs first and is not stamped', async ($, on) => {
  const feed = new Feed()
  const eng = engine($)
  const h = setup(on, { feeds: [feed], submit: eng.submit })
  await start($, h)
  await $.turn.start({ text: 'long task', turnId: 't1' })
  await $.prompt.submit(person('go', 't1'))
  feed.line(deliver('c1', 'go'))
  await flush()
  await complete($, 't1')
  await $.turn.start({ text: 'go', turnId: 't2' })
  await complete($, 't2')
  await eng.run('go')
  expect(rows(h)).toEqual([
    ['t1', undefined, undefined],
    ['t2', undefined, 'composer'],
    ['d:go', 'c1', 'plugin'],
  ])
})

test('a deliver submitted before a person\'s identical text keeps its stamp', async ($, on) => {
  const feed = new Feed()
  const eng = engine($)
  const h = setup(on, { feeds: [feed], submit: eng.submit })
  await start($, h)
  await $.turn.start({ text: 'long task', turnId: 't1' })
  feed.line(deliver('c1', 'go'))
  await flush()
  await $.prompt.submit(person('go', 't1'))
  await complete($, 't1')
  await eng.run('go')
  await complete($, 'd:go')
  await $.turn.start({ text: 'go', turnId: 't3' })
  await h.settle()
  expect(rows(h)).toEqual([
    ['t1', undefined, undefined],
    ['d:go', 'c1', 'plugin'],
    ['t3', undefined, 'composer'],
  ])
})

// Typed over a turn that still steps, a prompt enters the session at the
// next step and never starts a turn of its own; it must not take the start
// of the next turn.
test('a prompt that folded into the running turn does not take a later deliver\'s start', async ($, on) => {
  const feed = new Feed()
  const eng = engine($)
  const h = setup(on, { feeds: [feed], submit: eng.submit })
  answerSteps(on)
  await start($, h)
  await $.turn.start({ text: 'long task', turnId: 't1' })
  await step($, 't1', 0)
  await $.prompt.submit(person('same words', 't1'))
  await step($, 't1', 1)
  await complete($, 't1')
  feed.line(deliver('c1', 'same words'))
  await flush()
  await eng.run('same words')
  expect(rows(h)).toEqual([
    ['t1', undefined, undefined],
    ['d:same words', 'c1', 'plugin'],
  ])
})

// A prompt a hook refused starts no turn.
test('a prompt that was refused does not take a later deliver\'s start', async ($, on) => {
  const feed = new Feed()
  const eng = engine($)
  const h = setup(on, { feeds: [feed], submit: eng.submit })
  await start($, h)
  await $.prompt.submit(person('refused'))
  feed.line(deliver('c1', 'refused'))
  await flush()
  await eng.run('refused')
  expect(rows(h)).toEqual([['d:refused', 'c1', 'plugin']])
})

// The prompt's origin says who started a turn nobody at leo sent.
test('a turn a person or a wake started reports who submitted it', async ($, on) => {
  const h = setup(on, { submit: engine($).submit })
  await start($, h)
  await $.prompt.submit(person('hello'))
  await complete($, 'p:hello')
  await $.prompt.submit(person('<task-notification/>', undefined, 'task-notification'))
  expect(rows(h)).toEqual([
    ['p:hello', undefined, 'composer'],
    ['p:<task-notification/>', undefined, 'task-notification'],
  ])
})

// The mod's own submit raises no hook for the mod: nothing of it may linger
// to claim the next turn nobody submitted.
test('a turn nobody submitted after a deliver\'s turn carries no stamp', async ($, on) => {
  const feed = new Feed()
  const eng = engine($)
  const h = setup(on, { feeds: [feed], submit: eng.submit })
  await start($, h)
  feed.line(deliver('c1', 'go'))
  await flush()
  await eng.run('go')
  await complete($, 'd:go')
  await $.turn.start({ text: 'a continuation', turnId: 't2' })
  await h.settle()
  expect(rows(h)).toEqual([
    ['d:go', 'c1', 'plugin'],
    ['t2', undefined, undefined],
  ])
})

// The queue entry of a prompt typed over a running turn is released when that
// turn completes; the prompt's hook may still resolve as refused after that.
test('a queued prompt refused after its running turn completed leaves nothing behind', async ($, on) => {
  let refuse: (() => void) | null = null
  const h = setup(on, {
    submit: async (e) => {
      await new Promise<void>((r) => (refuse = r))
      return { drop: 'refused', text: e.text }
    },
  })
  await start($, h)
  await $.turn.start({ text: 'long task', turnId: 't1' })
  const refused = $.prompt.submit(person('typed over it', 't1'))
  await flush()
  await complete($, 't1')
  refuse!()
  await refused
  await $.turn.start({ text: 'a continuation', turnId: 't2' })
  await h.settle()
  expect(rows(h)).toEqual([
    ['t1', undefined, undefined],
    ['t2', undefined, undefined],
  ])
})

// A /clear or a resume ends the session the prompts were typed in.
test('prompts pending when a session ends are forgotten', async ($, on) => {
  const h = setup(on, { submit: engine($).submit })
  await start($, h)
  await $.turn.start({ text: 'long task', turnId: 't1' })
  await $.prompt.submit(person('typed over it', 't1'))
  await $.session.end({ reason: 'clear', sessionId: 'sess-1', resume: {} as any })
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })
  await complete($, 't1')
  await $.turn.start({ text: 'a continuation', turnId: 't2' })
  await h.settle()
  expect(rows(h)).toEqual([
    ['t1', undefined, undefined],
    ['t2', undefined, undefined],
  ])
})

test('prompts pending when a session starts are forgotten', async ($, on) => {
  const h = setup(on, { submit: engine($).submit })
  await start($, h)
  await $.turn.start({ text: 'long task', turnId: 't1' })
  await $.prompt.submit(person('typed over it', 't1'))
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })
  await complete($, 't1')
  await $.turn.start({ text: 'a continuation', turnId: 't2' })
  await h.settle()
  expect(rows(h)).toEqual([
    ['t1', undefined, undefined],
    ['t2', undefined, undefined],
  ])
})

// A clear ends the session the deliver was submitted in: its submit settling
// later is no claim on the new session's turns, which are the people's and
// the wakes' to start, and its own turn (if it still runs) goes unstamped.
test('a deliver in flight across a clear loses its stamp and takes no later turn', async ($, on) => {
  const feed = new Feed()
  const eng = engine($)
  const h = setup(on, { feeds: [feed], submit: eng.submit })
  await start($, h)
  feed.line(deliver('c1', 'go'))
  await flush()
  await $.session.end({ reason: 'clear', sessionId: 'sess-1', resume: {} as any })
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })
  await $.prompt.submit(person('hello'))
  await complete($, 'p:hello')
  await eng.run('go')
  expect(rows(h)).toEqual([
    ['p:hello', undefined, 'composer'],
    ['d:go', undefined, undefined],
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
