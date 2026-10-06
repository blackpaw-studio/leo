import { expect, test } from 'claude-code/testing'
import { AGENT_DENY_TEXT, DELEGATION_SECTION_ID, LEO_DISPATCH_TOOL } from '../hooks/roster.js'
import { advance, AGENT, Feed, setup, start } from './harness.ts'

const ON = { enabled: true, section: 'Delegation roles:\n- implement', hide_agents: ['implementer', 'Explore', 'Plan'] }
const OFF = { enabled: false, section: '', hide_agents: [] }
const stateLine = (delegation: object, dispatches: object[] = []) => ({ op: 'state', delegation, dispatches })

const ENGINE = { plugin: 'engine', tier: 'core' }
const offer = ($: any, agent: string) => $.agent.offer({ agent, description: '', source: 'built-in', provider: ENGINE })
const callAgent = ($: any, subagentType?: string) =>
  $.tool.call(subagentType === undefined ? { tool: 'Agent', prompt: 'p', description: 'd' } : { tool: 'Agent', prompt: 'p', description: 'd', subagent_type: subagentType })
const COMPOSE = { model: 'opus', promptModel: 'opus', surfaces: ['terminal'], tools: [], outputStyle: null, traits: [] }
const compose = ($: any, traits: string[] = []) => $.prompt.compose({ ...COMPOSE, traits })
// The test kit never routes a plugin's $.session.append to a test's hooks
// (the call fails "no implementation"), so a note shows as the mod's log of
// that failure, which names the attempt. Live on 2.1.292 the append lands and
// the model reads it on its next turn.
const notes = (h: any) => h.logs.filter((l: string) => l.startsWith('adding the delegation note failed'))
const sectionOf = (composed: any) => composed.sections.find((s: any) => s.id === DELEGATION_SECTION_ID)

async function started($: any, on: any, delegation: object = ON, opts: object = {}) {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed], ...opts })
  await start($, h)
  feed.line(stateLine(delegation))
  await h.settle()
  return { h, feed }
}

test('the delegation section is composed in while delegation is on', async ($, on) => {
  await started($, on)
  const composed = await compose($)
  expect(sectionOf(composed)).toEqual({ id: DELEGATION_SECTION_ID, text: ON.section, scope: 'session' })
  expect(composed.sections[0]).toMatchObject({ id: 'intro' })
})

test('no section while delegation is off or before any state', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed] })
  await start($, h)
  expect(sectionOf(await compose($))).toBeUndefined()
  feed.line(stateLine(OFF))
  await h.settle()
  expect(sectionOf(await compose($))).toBeUndefined()
})

// The engine keeps the system prompt it composed first (verified live on
// 2.1.292), so a change after that reaches the model as a note.
test('a delegation change after the prompt was composed invalidates it and notes the change', async ($, on) => {
  const { h, feed } = await started($, on)
  await compose($)
  feed.line(stateLine(OFF))
  await h.settle()
  expect(h.invalidates).toContain('prompt.section')
  expect(notes(h)).toHaveLength(1)

  feed.line(stateLine({ ...ON, section: 'Delegation roles:\n- review' }))
  await h.settle()
  expect(notes(h)).toHaveLength(2)
  // The roster still refreshed behind the failed note.
  expect(h.invalidates.at(-1)).toBe('ui.render')
})

test('a change before the first compose needs no note, and a repeat changes nothing', async ($, on) => {
  const { h, feed } = await started($, on, OFF)
  feed.line(stateLine(ON))
  await h.settle()
  expect(notes(h)).toHaveLength(0)
  expect(sectionOf(await compose($))).toBeDefined()
  const promptInvalidates = () => h.invalidates.filter((x) => x === 'prompt.section').length
  const invalidated = promptInvalidates()
  feed.line(stateLine(ON, [{ id: 'd1', status: 'running' }]))
  await h.settle()
  expect(notes(h)).toHaveLength(0)
  expect(promptInvalidates()).toBe(invalidated)
})

test('a /context measure does not count as the composed prompt', async ($, on) => {
  const { h, feed } = await started($, on, OFF)
  await compose($, ['analysis'])
  feed.line(stateLine(ON))
  await h.settle()
  expect(notes(h)).toHaveLength(0)
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

// A hot reload resets the module, but the engine keeps the system prompt
// (and any note) the model was given: what the model was told outlives it.
const TOLD_KEY = 'told:' + AGENT
const toldEntry = (session: string, d: { enabled: boolean; section: string }) => ({ session, enabled: d.enabled, section: d.section })

test('what the model was told is stored per session when the prompt is composed', async ($, on) => {
  const { h } = await started($, on)
  await compose($)
  await h.settle()
  expect(h.store.get(TOLD_KEY)).toEqual(toldEntry('sess-1', ON))
})

test('after a reload, ON then OFF sends exactly one note', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed], store: { [TOLD_KEY]: toldEntry('sess-1', ON) } })
  await start($, h)
  feed.line(stateLine(ON))
  feed.line(stateLine(OFF))
  await h.settle()
  expect(notes(h)).toHaveLength(1)
  expect(h.store.get(TOLD_KEY)).toEqual(toldEntry('sess-1', OFF))
})

// The reloaded module composes again, but the engine keeps its first
// prompt: that compose must not pass for what the model was told.
test('a compose after a reload does not overwrite what the model was told', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed], store: { [TOLD_KEY]: toldEntry('sess-1', ON) } })
  await start($, h)
  feed.line(stateLine(OFF))
  await h.settle()
  await compose($)
  expect(notes(h)).toHaveLength(1)
  feed.line(stateLine(ON))
  await h.settle()
  expect(notes(h)).toHaveLength(2)
})

test('what another session was told does not count for this one', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed], store: { [TOLD_KEY]: toldEntry('sess-0', ON) } })
  await start($, h)
  feed.line(stateLine(OFF))
  await h.settle()
  expect(notes(h)).toHaveLength(0)
})

// A snapshot read off a stream that has since dropped is stale: applying it
// must not take back the fallback the drop set.
test('a snapshot applied after its stream dropped keeps native agents allowed', async ($, on) => {
  let isArmed = false
  let release: (() => void) | null = null
  const feed = new Feed()
  const h = setup(on, {
    feeds: [feed],
    beforeStoreSet: (key) => {
      if (key !== TOLD_KEY || !isArmed || release !== null) return undefined
      return new Promise<void>((r) => (release = r))
    },
  })
  await start($, h)
  feed.line(stateLine(OFF))
  await h.settle()
  await compose($)
  await h.settle()
  isArmed = true
  // The first change stalls writing what the model is told; the second waits
  // behind it, and the stream drops meanwhile.
  feed.line(stateLine(ON))
  feed.line(stateLine({ ...ON, section: 'Delegation roles:\n- review' }))
  feed.end(1)
  await h.settle()
  expect(release).not.toBeNull()
  release!()
  await h.settle()
  expect(await callAgent($, 'Explore')).toMatchObject({ result: 'ran' })
  expect(h.statuses.at(-1)).toContain('native agents allowed: leo bridge down')
})

// A broken guard must not block all delegation: it logs and lets the call
// through, as leo being down does.
test('a guard that throws logs and allows the agent', async ($, on) => {
  const { h } = await started($, on)
  expect(await callAgent($, 42 as any)).toMatchObject({ result: 'ran' })
  expect(h.logs.some((l: string) => l.startsWith('delegation guard failed'))).toBe(true)
})
