import { expect, test } from 'claude-code/testing'
import { MAX_REPORT_TEXT_CHARS, REPORT_RETRY_DELAYS_MS, reportText } from '../hooks/protocol.js'
import { advance, AGENT, BIN, events, Feed, LAUNCH, REPORT_ARGV, setup, start } from './harness.ts'

// leo sets LEO_BRIDGE_AGENT to the bridge key it routes by: the agent name
// for agents, a dispatch-unique key for dispatches. LEO_PROCESS_NAME is the
// process's display name and must not be mistaken for it.
test('the bridge key comes from LEO_BRIDGE_AGENT, not LEO_PROCESS_NAME', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, {
    feeds: [feed],
    env: {
      LEO_BRIDGE_BIN: BIN,
      LEO_BRIDGE_AGENT: 'dispatch.d-0123456789ab',
      LEO_BRIDGE_LAUNCH: LAUNCH,
      LEO_PROCESS_NAME: 'leo-other',
    },
  })
  await start($, h)
  expect(h.spawns).toEqual([[BIN, 'bridge', '--agent', 'dispatch.d-0123456789ab', '--launch', LAUNCH]])
  expect(h.reportArgv[0]).toEqual([BIN, 'bridge', 'report', '--agent', 'dispatch.d-0123456789ab', '--launch', LAUNCH])
})

test('LEO_PROCESS_NAME alone leaves the bridge disabled', async ($, on) => {
  const h = setup(on, { env: { LEO_BRIDGE_BIN: BIN, LEO_BRIDGE_LAUNCH: LAUNCH, LEO_PROCESS_NAME: AGENT } })
  await start($, h)
  expect(h.spawns).toEqual([])
  expect(h.logs.length).toBe(1)
  expect(h.logs[0]).toContain('LEO_BRIDGE_AGENT')
})

// Reports can carry a whole prompt or answer, far past what argv holds, so
// the JSON travels on stdin and argv stays fixed.
test('reports travel on stdin; argv carries no payload', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.turn.start({ text: 'x'.repeat(200_000), turnId: 't1' })
  await h.settle()
  for (const argv of h.reportArgv) expect(argv).toEqual(REPORT_ARGV)
  for (const stdin of h.reportStdin) expect(typeof stdin).toBe('string')
  expect(JSON.parse(h.reportStdin[0]!)).toMatchObject({ type: 'hello' })
})

test('turn.start carries the prompt; turn.complete carries the final message', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.turn.start({ text: 'line one\nline two', turnId: 't1' })
  await $.turn.complete({ turnId: 't1', answer: 'the answer', durationMs: 5, isAborted: false, reason: 'answer' })
  await h.settle()
  const [startEv, completeEv] = events(h)
  expect(startEv).toEqual({ type: 'event', name: 'turn.start', event_id: 'turn.start:t1', prompt: 'line one\nline two' })
  expect(completeEv).toMatchObject({ type: 'event', name: 'turn.complete', message: 'the answer' })
})

// An interrupted turn still completes; leo closes it as interrupted rather
// than finished, so the report says so.
test('an aborted turn.complete says reason aborted; a finished one has no reason', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.turn.start({ text: 'go', turnId: 't1' })
  await $.turn.complete({ turnId: 't1', answer: 'partial', durationMs: 5, isAborted: true, reason: 'aborted' })
  await $.turn.start({ text: 'again', turnId: 't2' })
  await $.turn.complete({ turnId: 't2', answer: 'done', durationMs: 5, isAborted: false, reason: 'answer' })
  await h.settle()
  const [, aborted, , finished] = events(h)
  expect(aborted).toMatchObject({ name: 'turn.complete', reason: 'aborted', message: 'partial' })
  expect(finished!.reason).toBeUndefined()
})

test('missing prompt or answer text is left out, not sent as junk', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.turn.start({ turnId: 't1' } as any)
  await $.turn.complete({ turnId: 't1', durationMs: 5, isAborted: true, reason: 'aborted' } as any)
  await h.settle()
  const [startEv, completeEv] = events(h)
  expect(startEv).toEqual({ type: 'event', name: 'turn.start', event_id: 'turn.start:t1' })
  expect(completeEv!.message).toBeUndefined()
})

test('oversized prompt and message are capped', async ($, on) => {
  const h = setup(on)
  await start($, h)
  const big = 'y'.repeat(MAX_REPORT_TEXT_CHARS + 10)
  await $.turn.start({ text: big, turnId: 't1' })
  await $.turn.complete({ turnId: 't1', answer: big, durationMs: 5, isAborted: false, reason: 'answer' })
  await h.settle()
  const [startEv, completeEv] = events(h)
  expect((startEv!.prompt as string).length).toBe(MAX_REPORT_TEXT_CHARS)
  expect((completeEv!.message as string).length).toBe(MAX_REPORT_TEXT_CHARS)
})

test('reportText keeps strings (capped) and drops anything else', () => {
  expect(reportText('hi')).toBe('hi')
  expect(reportText('')).toBe('')
  expect(reportText(undefined)).toBeUndefined()
  expect(reportText(42)).toBeUndefined()
  expect(reportText('z'.repeat(MAX_REPORT_TEXT_CHARS + 1))!.length).toBe(MAX_REPORT_TEXT_CHARS)
})

// A report the daemon applied but whose reply was lost is retried; the event
// id, derived from the turn or session, lets the daemon drop the replay.
test('a retried event carries the same event id', { timeoutMs: 20_000 }, async ($, on) => {
  const h = setup(on, { reportExit: (n) => (n === 2 ? 1 : 0) })
  await start($, h)
  await $.turn.start({ text: 'go', turnId: 't7' })
  await h.settle()
  await advance(h, REPORT_RETRY_DELAYS_MS[0]!)
  const tries = h.attempts.filter((r) => r.name === 'turn.start')
  expect(tries.length).toBe(2)
  expect(tries.map((r) => r.event_id)).toEqual(['turn.start:t7', 'turn.start:t7'])
})

test('an event without a usable id source carries no event id', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.turn.start({ text: 'go', turnId: '' } as any)
  await h.settle()
  expect(events(h)[0]).toEqual({ type: 'event', name: 'turn.start', prompt: 'go' })
})
