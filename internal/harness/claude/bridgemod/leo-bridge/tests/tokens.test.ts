import { expect, test } from 'claude-code/testing'
import { turnTokens } from '../hooks/protocol.js'
import { events, setup, start } from './harness.ts'

const USAGE = {
  input_tokens: 120,
  output_tokens: 45,
  cache_read_input_tokens: 9000,
  cache_creation_input_tokens: 300,
  model: 'claude-opus-5-5',
}

test('turnTokens maps the engine usage to the report shape', () => {
  expect(turnTokens(USAGE)).toEqual({ input: 120, output: 45, cache_read: 9000, cache_creation: 300, model: 'claude-opus-5-5' })
})

// The engine leaves usage out when nothing counted (an interrupt, an API
// error): the turn cost nothing, which is a count, not an unknown.
test('turnTokens counts a turn without usage as zero', () => {
  expect(turnTokens(undefined)).toEqual({ input: 0, output: 0, cache_read: 0, cache_creation: 0 })
})

test('turnTokens drops counts that are not non-negative numbers', () => {
  expect(turnTokens({ ...USAGE, input_tokens: -1, output_tokens: 'x', model: 7 })).toEqual({
    input: 0,
    output: 0,
    cache_read: 9000,
    cache_creation: 300,
  })
})

test('turn.complete carries the turn token counts', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.turn.start({ text: 'go', turnId: 't1' })
  await $.turn.complete({ turnId: 't1', answer: 'done', durationMs: 5, isAborted: false, reason: 'answer', usage: USAGE })
  await h.settle()
  const complete = events(h).find((r) => r.name === 'turn.complete')
  expect(complete?.tokens).toEqual({ input: 120, output: 45, cache_read: 9000, cache_creation: 300, model: 'claude-opus-5-5' })
  // Cost still comes from the session usage, as before.
  expect(complete?.usage).toMatchObject({ cost: { usd: 0.01 } })
})

test('a subagent turn.complete reports no tokens', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.turn.complete({ turnId: 's1', agentId: 'sub', answer: '', durationMs: 5, isAborted: false, reason: 'answer', usage: USAGE })
  await h.settle()
  expect(events(h)).toEqual([])
})
