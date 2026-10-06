import { expect, test } from 'claude-code/testing'
import { advance, Feed, REPORT_ARGV, setup, start } from './harness.ts'

const OFF = { enabled: false, section: '', hide_agents: [] }
const RUNNING = { id: 'd1', name: 'impl-a', role: 'implement', template: 'codex', model: 'gpt-5', status: 'running', stalled: false, active_seconds: 65, tokens_in: 1200, tokens_out: 340, cost_usd: 0.42 }
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

test('the band draws a row per dispatch, with Cancel on live ones', async ($, on) => {
  await withState($, on, [RUNNING, IDLE, DONE])
  const ui = await mount($)
  const run = await ui.find({ type: 'Text', text: /impl-a/ })
  expect(run?.text).toContain('gpt-5')
  expect(run?.text).toContain('running')
  expect(run?.text).toContain('1:05')
  expect(run?.text).toContain('1.2k/340')
  expect(run?.text).toContain('$0.42')
  const idle = await ui.find({ type: 'Text', text: /review/ })
  expect(idle?.text).toContain('idle · stalled')
  expect(await ui.find({ key: 'cancel-d1' })).toBeDefined()
  expect(await ui.find({ key: 'cancel-d2' })).toBeDefined()
  expect(await ui.find({ key: 'cancel-d3' })).toBeUndefined()
  await ui.unmount()
})

test('without dispatches the band is left to the engine', async ($, on) => {
  await withState($, on, [])
  const ui = await mount($)
  expect(await ui.find({ type: 'Button' })).toBeUndefined()
  expect(await ui.find({ type: 'Text', text: /running/ })).toBeUndefined()
  await ui.unmount()
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

test('the status line counts running and idle dispatches and clears with none', async ($, on) => {
  const { h, feed } = await withState($, on, [RUNNING, IDLE, DONE])
  expect(h.statuses.at(-1)).toBe('⇢ 1 running · 1 idle')
  feed.line(stateLine([DONE]))
  await h.settle()
  expect(h.statuses.at(-1)).toBeUndefined()
})
