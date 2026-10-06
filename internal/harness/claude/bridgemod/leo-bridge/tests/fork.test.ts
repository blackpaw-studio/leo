import { expect, test } from 'claude-code/testing'
import { forkConsultAnswer } from '../hooks/protocol.js'
import { setup, start } from './harness.ts'

const TOOL = 'mcp__leo__leo_consult'
const USAGE = { input_tokens: 12, output_tokens: 34, cache_read_input_tokens: 5600, cache_creation_input_tokens: 0 }

// Stubs the engine beneath the mod: the fork, the main loop's model, and
// the leo MCP server (which a fork consult must never reach).
function engine(on: any, fork: () => unknown) {
  const forks: Array<Record<string, unknown>> = []
  const serverCalls: Array<Record<string, unknown>> = []
  on('model.fork', ($: any, e: any) => {
    forks.push({ ...e })
    return { value: fork() }
  })
  on('session.model', () => ({ value: 'claude-opus-5-5' }))
  on('tool.call', ($: any, e: any) => {
    serverCalls.push({ ...e })
    return { result: 'from the leo server' }
  })
  return { forks, serverCalls }
}

test('forkConsultAnswer formats an answered fork', () => {
  const answer = forkConsultAnswer({ isAnswered: true, text: 'Looks sound.', usage: USAGE }, 'claude-opus-5-5')
  expect(answer).toEqual({ result: '[consult · fork/claude-opus-5-5] tokens 12/34/5600\nLooks sound.' })
})

test('forkConsultAnswer names the reason a fork has no answer', () => {
  expect(forkConsultAnswer({ isAnswered: false, reason: 'nothing-to-fork' }, 'm')).toEqual({
    deny: 'fork consult failed: nothing-to-fork',
  })
  const apiError = forkConsultAnswer({ isAnswered: false, reason: 'api-error', status: 529, error: 'overloaded', usage: USAGE }, 'm')
  expect(apiError).toEqual({ deny: 'fork consult failed: api-error (529 overloaded)' })
})

test('a fork consult is answered by the mod from a fork of the session', async ($, on) => {
  const h = setup(on)
  const eng = engine(on, () => ({ isAnswered: true, text: 'Ship it.', usage: USAGE }))
  await start($, h)
  const result = await $.tool.call({ tool: TOOL, prompt: 'Is this plan sane?', fork: true, template: 'ignored' } as any)
  expect(eng.forks).toEqual([{ prompt: 'Is this plan sane?' }])
  expect(eng.serverCalls).toEqual([])
  expect(result.result).toBe('[consult · fork/claude-opus-5-5] tokens 12/34/5600\nShip it.')
})

test('a fork with no answer is an error result naming the reason', async ($, on) => {
  const h = setup(on)
  const eng = engine(on, () => ({ isAnswered: false, reason: 'empty-reply', usage: USAGE }))
  await start($, h)
  const result: any = await $.tool.call({ tool: TOOL, prompt: 'Thoughts?', fork: true } as any)
  expect(eng.serverCalls).toEqual([])
  // The model reads a deny as the call's error result.
  expect(result.deny).toBe('fork consult failed: empty-reply')
})

test('a consult without fork goes to the leo server', async ($, on) => {
  const h = setup(on)
  const eng = engine(on, () => ({ isAnswered: true, text: 'unused', usage: USAGE }))
  await start($, h)
  const result = await $.tool.call({ tool: TOOL, prompt: 'Thoughts?', template: 'codex' } as any)
  expect(eng.forks).toEqual([])
  expect(eng.serverCalls.length).toBe(1)
  expect(result.result).toBe('from the leo server')
})

// A fork sees the main conversation, not a subagent's: answering a
// subagent with it would answer a question about the wrong context.
test('a subagent fork consult is refused', async ($, on) => {
  const h = setup(on)
  const eng = engine(on, () => ({ isAnswered: true, text: 'unused', usage: USAGE }))
  await start($, h)
  const result: any = await $.tool.call({ tool: TOOL, prompt: 'Thoughts?', fork: true, agentId: 'sub' } as any)
  expect(eng.forks).toEqual([])
  expect(eng.serverCalls).toEqual([])
  expect(String(result.deny)).toContain('main conversation')
})
