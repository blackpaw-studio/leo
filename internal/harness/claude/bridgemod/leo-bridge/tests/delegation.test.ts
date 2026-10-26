import { expect, test } from 'claude-code/testing'
import { AGENT_DENY_TEXT, LEO_DISPATCH_TOOL } from '../hooks/roster.js'
import { advance, AGENT, Feed, setup, start } from './harness.ts'

const ON = { enabled: true, section: 'Delegation roles:\n- implement', hide_agents: ['implementer', 'Explore', 'Plan'] }
const OFF = { enabled: false, section: '', hide_agents: [] }
const stateLine = (delegation: object, dispatches: object[] = []) => ({ op: 'state', delegation, dispatches })

const ENGINE = { plugin: 'engine', tier: 'core' }
const offer = ($: any, agent: string) => $.agent.offer({ agent, description: '', source: 'built-in', provider: ENGINE })
const callAgent = ($: any, subagentType?: string) =>
  $.tool.call(subagentType === undefined ? { tool: 'Agent', prompt: 'p', description: 'd' } : { tool: 'Agent', prompt: 'p', description: 'd', subagent_type: subagentType })
async function started($: any, on: any, delegation: object = ON, opts: object = {}) {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed], ...opts })
  await start($, h)
  feed.line(stateLine(delegation))
  await h.settle()
  return { h, feed }
}

// The delegation text reaches the model through Claude command hooks
// (`leo dispatch context`), not through the mod: prompt.compose and
// $.session.append are mods API events cc-plugin-sec-default passes over.
test('a delegation change tells the model nothing through the mod', async ($, on) => {
  const { h, feed } = await started($, on)
  feed.line(stateLine(OFF))
  await h.settle()
  expect(h.logs.filter((l) => l.includes('note'))).toEqual([])
  expect(h.invalidates).not.toContain('prompt.section')
})

test('agent.offer hides only the listed types while delegation is on', async ($, on) => {
  const { h, feed } = await started($, on)
  expect(await offer($, 'Explore')).toEqual({ isOffered: false })
  expect(await offer($, 'some-plugin:implementer')).toEqual({ isOffered: false })
  expect(await offer($, 'claude-code-guide')).toEqual({ isOffered: true })
  feed.line(stateLine(OFF))
  await h.settle()
  expect(await offer($, 'Explore')).toEqual({ isOffered: true })
})

test('an Agent call to a hidden, default or general-purpose type is denied', async ($, on) => {
  const { h } = await started($, on)
  for (const type of ['Explore', undefined, 'general-purpose']) {
    expect(await callAgent($, type)).toEqual({ deny: AGENT_DENY_TEXT })
  }
  expect(h.toolCalls).toHaveLength(0)
  expect(await callAgent($, 'claude-code-guide')).toMatchObject({ result: 'ran' })
})

test('a failed leo_dispatch lets native agents through until one succeeds', async ($, on) => {
  let isFailing = true
  const { h } = await started($, on, ON, {
    toolCall: (e: any) => (e.tool === LEO_DISPATCH_TOOL && isFailing ? { result: 'boom', text: 'boom', isError: true } : { result: 'ran', text: 'ran' }),
  })
  await $.tool.call({ tool: LEO_DISPATCH_TOOL, role: 'implement', prompt: 'x' })
  await h.settle()
  expect(await callAgent($, 'Explore')).toMatchObject({ result: 'ran' })
  expect(await offer($, 'Explore')).toEqual({ isOffered: true })
  expect(h.statuses.at(-1)).toContain('native agents allowed: leo_dispatch failed')

  isFailing = false
  await $.tool.call({ tool: LEO_DISPATCH_TOOL, role: 'implement', prompt: 'x' })
  await h.settle()
  expect(await callAgent($, 'Explore')).toEqual({ deny: AGENT_DENY_TEXT })
  expect(h.statuses.at(-1)).toBeUndefined()
})

test('no state within ten seconds of session start falls back', async ($, on) => {
  const h = setup(on, { feeds: [new Feed()] })
  await start($, h)
  await advance(h, 10_000)
  expect(h.statuses.at(-1)).toContain('native agents allowed: no state from leo')
})

test('a stream that drops falls back until the next state', async ($, on) => {
  const first = new Feed()
  const second = new Feed()
  const h = setup(on, { feeds: [first, second] })
  await start($, h)
  first.line(stateLine(ON))
  await h.settle()
  first.end(1)
  await h.settle()
  expect(await callAgent($, 'Explore')).toMatchObject({ result: 'ran' })
  expect(h.statuses.at(-1)).toContain('native agents allowed: leo bridge down')

  await advance(h, 1000)
  second.line(stateLine(ON))
  await h.settle()
  expect(await callAgent($, 'Explore')).toEqual({ deny: AGENT_DENY_TEXT })
})

// A broken guard must not block all delegation: it logs and lets the call
// through, as leo being down does.
test('a guard that throws logs and allows the agent', async ($, on) => {
  const { h } = await started($, on)
  expect(await callAgent($, 42 as any)).toMatchObject({ result: 'ran' })
  expect(h.logs.some((l: string) => l.startsWith('delegation guard failed'))).toBe(true)
})
