import { expect, test } from 'claude-code/testing'
import { advance, Feed, REPORT_ARGV, setup, start } from './harness.ts'

const OFF = { enabled: false, section: '', hide_agents: [] }
const RUNNING = { id: 'd1', name: 'impl-a', role: 'implement', template: 'codex', model: 'gpt-5', effort: 'high', status: 'running', stalled: false, active_seconds: 65, tokens_in: 1200, tokens_out: 340, cost_usd: 0.42 }
const IDLE = { id: 'd2', name: '', role: 'review', template: 'claude', model: 'opus', status: 'idle', stalled: true, active_seconds: 10 }
const DONE = { id: 'd3', name: 'old', role: '', template: 'codex', model: 'gpt-5', status: 'done', stalled: false, active_seconds: 30 }
const stateLine = (dispatches: object[]) => ({ op: 'state', delegation: OFF, dispatches })

const BAND = {
  component: 'AbovePrompt',
  props: { hasSurvey: false, isWorking: false, maxRows: 10, bodyColumns: 100, scroll: { offset: 0, bodyRows: 9 }, view: {} },
} as const

async function withState($: any, on: any, dispatches: object[], opts: object = {}) {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed], ...opts })
  await start($, h)
  feed.line(stateLine(dispatches))
  await h.settle()
  return { h, feed }
}

const mount = ($: any) => $.ui.mount({ plugin: 'leo-bridge', surface: 'terminal', ...BAND })

test('the band draws a gap, a header, then a row per dispatch, with Cancel on live ones', async ($, on) => {
  await withState($, on, [RUNNING, IDLE, DONE])
  const ui = await mount($)
  const tree: any = await ui.drawn()
  expect(tree.children[0]).toMatchObject({ type: 'Text', children: [' '] })
  expect(tree.children[1]).toMatchObject({ type: 'Text', props: { dimColor: true } })
  const header = await ui.find({ type: 'Text', text: /^leo dispatches/ })
  expect(header?.text).toBe('leo dispatches ' + '─'.repeat(85))
  const run = await ui.find({ type: 'Text', text: /impl-a/ })
  expect(run?.text).toMatch(/^  ⟳ impl-a  gpt-5 · high  1:05 +1\.2k\/340  \$0\.42$/)
  expect(run?.text).not.toContain('running')
  const idle = await ui.find({ type: 'Text', text: /review/ })
  expect(idle?.text).toContain('0:10 stalled')
  expect(idle?.text).toMatch(/opus  /)
  expect(idle?.text).not.toContain('opus ·')
  expect(await ui.find({ key: 'cancel-d1' })).toBeDefined()
  expect(await ui.find({ key: 'cancel-d2' })).toBeDefined()
  expect(await ui.find({ type: 'Text', text: /old/ })).toBeUndefined()
  await ui.unmount()
})

test('without dispatches the band is left to the engine', async ($, on) => {
  await withState($, on, [])
  const ui = await mount($)
  expect(await ui.find({ type: 'Button' })).toBeUndefined()
  expect(await ui.find({ type: 'Text', text: /^leo dispatches/ })).toBeUndefined()
  expect(await ui.find({ type: 'Text', text: /^ $/ })).toBeUndefined()
  await ui.unmount()
})

test('a dispatch terminal at load is never shown, so the band stays the engine\'s', async ($, on) => {
  await withState($, on, [DONE])
  const ui = await mount($)
  expect(await ui.find({ type: 'Text', text: /^leo dispatches/ })).toBeUndefined()
  await ui.unmount()
})

test('a dispatch that ends stays 10 s without Cancel, then leaves and the ticker stops', async ($, on) => {
  const { h, feed } = await withState($, on, [RUNNING])
  feed.line(stateLine([{ ...RUNNING, status: 'done' }]))
  await h.settle()
  let ui = await mount($)
  expect((await ui.find({ type: 'Text', text: /impl-a/ }))?.text).toContain('✓')
  expect(await ui.find({ key: 'cancel-d1' })).toBeUndefined()
  await ui.unmount()
  await advance(h, 9000)
  ui = await mount($)
  expect(await ui.find({ type: 'Text', text: /impl-a/ })).toBeDefined()
  await ui.unmount()
  const ticking = h.invalidates.length
  await advance(h, 2000)
  expect(h.invalidates.length).toBeGreaterThan(ticking)
  ui = await mount($)
  expect(await ui.find({ type: 'Text', text: /^leo dispatches/ })).toBeUndefined()
  await ui.unmount()
  const stopped = h.invalidates.length
  await advance(h, 3000)
  expect(h.invalidates.length).toBe(stopped)
})

test('a running dispatch keeps counting between snapshots', async ($, on) => {
  const { h } = await withState($, on, [RUNNING])
  await advance(h, 5000)
  const ui = await mount($)
  expect((await ui.find({ type: 'Text', text: /impl-a/ }))?.text).toContain('1:10')
  await ui.unmount()
})

test('Cancel sends a dispatch.cancel request and toasts the outcome', async ($, on) => {
  const { h } = await withState($, on, [RUNNING])
  const ui = await mount($)
  await ui.press({ key: 'cancel-d1' })
  await h.settle()
  const request = h.attempts.find((r) => r?.type === 'request')
  expect(request).toEqual({ type: 'request', op: 'dispatch.cancel', dispatch_id: 'd1' })
  expect(h.reportArgv.at(-1)).toEqual(REPORT_ARGV)
  expect(h.toasts.at(-1)).toContain('canceling impl-a')
  await ui.unmount()
})

test('a refused Cancel toasts why', async ($, on) => {
  const { h } = await withState($, on, [RUNNING], { reportExit: (call: number) => (call > 1 ? 1 : 0) })
  const ui = await mount($)
  await ui.press({ key: 'cancel-d1' })
  await h.settle()
  expect(h.toasts.at(-1)).toContain('cancel failed')
  await ui.unmount()
})

test('the status line stays empty with dispatches and no fallback', async ($, on) => {
  const { h } = await withState($, on, [RUNNING, IDLE, DONE])
  expect(h.statuses.filter((x) => x !== undefined)).toEqual([])
})
