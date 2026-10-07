import { expect, test } from 'claude-code/testing'
import { events, setup, start } from './harness.ts'

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
