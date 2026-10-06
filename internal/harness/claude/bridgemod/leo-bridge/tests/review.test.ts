import { expect, test } from 'claude-code/testing'
import { REPORT_RETRY_DELAYS_MS } from '../hooks/protocol.js'
import { acks, advance, events, Feed, setup, start } from './harness.ts'

const deliver = (id: string, text: string) => ({ id, op: 'deliver', text, as_user: false })

// Per the v2.1.289 typings a subagent's run raises no turn.start, but a hook
// that ever sees one carrying agentId must not mistake it for the main loop's.
test('a subagent turn.start neither marks the main loop busy nor reports', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed] })
  await start($, h)
  await $.turn.start({ text: '', turnId: 's1', agentId: 'sub' } as any)
  feed.line({ id: 'k1', op: 'compact' })
  feed.line({ id: 'i1', op: 'interrupt' })
  await h.settle()
  expect(h.compacts.length).toBe(1)
  expect(h.aborts).toEqual([])
  expect(events(h)).toEqual([])
})

test('a failed report is retried with backoff and later reports wait their turn', { timeoutMs: 20_000 }, async ($, on) => {
  const feed = new Feed()
  // Call 1 is the hello; call 2, the first try at c1's ack, fails.
  const h = setup(on, { feeds: [feed], reportExit: (n) => (n === 2 ? 1 : 0) })
  await start($, h)
  feed.line(deliver('c1', 'x'))
  feed.line(deliver('c2', 'y'))
  await h.settle()
  expect(h.submits.length).toBe(2)
  expect(acks(h)).toEqual([])
  await advance(h, REPORT_RETRY_DELAYS_MS[0]!)
  expect(acks(h).map((a) => a.id)).toEqual(['c1', 'c2'])
  expect(h.logs).toEqual([])
})

test('a report that keeps failing is dropped after the last retry, logged once', { timeoutMs: 30_000 }, async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed], reportExit: (n) => (n === 1 ? 0 : 1) })
  await start($, h)
  feed.line(deliver('c1', 'x'))
  await h.settle()
  for (const delay of REPORT_RETRY_DELAYS_MS) await advance(h, delay)
  const tries = h.attempts.filter((r) => r.type === 'ack').length
  expect(tries).toBe(REPORT_RETRY_DELAYS_MS.length + 1)
  expect(h.logs.length).toBe(1)
  expect(h.logs[0]).toContain('report failed')
})

test('turn.complete is still reported when usage cannot be read', async ($, on) => {
  const h = setup(on, {
    usage: () => {
      throw new Error('no cost ledger')
    },
  })
  await start($, h)
  await $.turn.start({ text: 'go', turnId: 't1' })
  await $.turn.complete({ turnId: 't1', answer: 'done', durationMs: 5, isAborted: false, reason: 'answer' })
  await h.settle()
  const complete = events(h).find((e) => e.name === 'turn.complete')
  expect(complete).toBeDefined()
  expect(complete!.usage).toBeUndefined()
})

test('hello says whether a main-loop turn is running', { timeoutMs: 10_000 }, async ($, on) => {
  const feeds = [new Feed(), new Feed()]
  const h = setup(on, { feeds })
  await start($, h)
  expect(h.reports[0]).toEqual({ type: 'hello', session_id: 'sess-1', claude_version: '2.1.289' })
  await $.turn.start({ text: 'work', turnId: 't1' })
  feeds[0]!.end()
  await advance(h, 1000)
  const hellos = h.reports.filter((r) => r.type === 'hello')
  expect(hellos.length).toBe(2)
  expect(hellos[1]).toMatchObject({ type: 'hello', busy: true })
})
