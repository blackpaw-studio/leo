import { expect, test } from 'claude-code/testing'
import { REJECTED_REPORT_EXIT_CODE, REPORT_RETRY_DELAYS_MS } from '../hooks/protocol.js'
import { advance, events, setup, start } from './harness.ts'

// The engine's own step: an empty answer.
function answerSteps(on: any) {
  on('turn.step', async function* (_$: any, e: any) {
    return { turnId: e.turnId, index: e.index, answer: '', toolUses: [], stopReason: 'end_turn', usage: null }
  })
}

async function step($: any, over: object) {
  const stream = $.turn.step({ turnId: 't1', index: 0, model: 'claude-opus-5-5', messageCount: 1, ...over })
  for await (const _ of stream) {
    // drain
  }
}

test('a main-loop step reports its effort once per change', async ($, on) => {
  const h = setup(on)
  answerSteps(on)
  await start($, h)
  await $.turn.start({ text: 'go', turnId: 't1' })
  await step($, { effort: 'high' })
  await step($, { effort: 'high', index: 1 })
  await step($, { effort: 'medium', index: 2 })
  await h.settle()
  expect(events(h).filter((r) => r.name === 'effort').map((r) => r.level)).toEqual(['high', 'medium'])
})

test('a subagent step, a numeric effort and a missing effort report nothing', async ($, on) => {
  const h = setup(on)
  answerSteps(on)
  await start($, h)
  await step($, { effort: 'low', agentId: 'sub' })
  await step($, { effort: 3 })
  await step($, {})
  await h.settle()
  expect(events(h).filter((r) => r.name === 'effort')).toEqual([])
})

// Sends turn t1's start, then an effort report the daemon answers with
// exit, then the turn's completion.
async function effortThenComplete($: any, on: any, exit: number) {
  let failing = -1
  const h = setup(on, { reportExit: (n) => (n === failing ? exit : 0) })
  answerSteps(on)
  await start($, h)
  await $.turn.start({ text: 'go', turnId: 't1' })
  await h.settle()
  failing = h.attempts.length + 1
  await step($, { effort: 'high' })
  await $.turn.complete({ turnId: 't1', answer: 'done', durationMs: 5, isAborted: false, reason: 'answer' })
  await h.settle()
  return h
}

// An older daemon refuses an event it does not know for good: the report is
// dropped at once, with one log line, and never holds up the next one.
test('a report the daemon rejects for good is dropped without retrying', async ($, on) => {
  const h = await effortThenComplete($, on, REJECTED_REPORT_EXIT_CODE)
  expect(h.attempts.filter((r) => r.name === 'effort').length).toBe(1)
  expect(h.reports.map((r) => r.name)).toContain('turn.complete')
  expect(h.logs.filter((l) => l.includes('rejected')).length).toBe(1)
})

// Any other failure is retried, holding the chain in order.
test('a report that fails transiently is retried', { timeoutMs: 20_000 }, async ($, on) => {
  const h = await effortThenComplete($, on, 1)
  expect(h.reports.map((r) => r.name)).not.toContain('turn.complete')
  await advance(h, REPORT_RETRY_DELAYS_MS[0]!)
  expect(h.attempts.filter((r) => r.name === 'effort').length).toBe(2)
  expect(h.reports.map((r) => r.name)).toContain('turn.complete')
})
