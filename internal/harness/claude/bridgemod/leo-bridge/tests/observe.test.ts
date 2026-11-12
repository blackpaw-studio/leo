import { expect, test } from 'claude-code/testing'
import { ACTIVITY_MIN_INTERVAL_MS } from '../hooks/protocol.js'
import { advance, AGENT, events, Feed, setup, start } from './harness.ts'

// Observe reports feed /api/v1: what the main loop is doing, whether it waits
// on the person, how many subagents run, and compaction phases.

function named(h: any, name: string) {
  return events(h).filter((r) => r.name === name)
}

function attempted(h: any, name: string) {
  return h.attempts.filter((r: any) => r?.type === 'event' && r.name === name)
}

// Tool calls the harness's engine holds open until the test releases them,
// by tool name ($.tool.call assigns its own tool_use_id).
function gates() {
  const open = new Map<string, () => void>()
  const toolCall = (e: any) => new Promise((resolve) => open.set(e.tool, () => resolve({ result: 'ran' })))
  const release = (tool: string) => open.get(tool)!()
  return { toolCall, release }
}

const bash = (id: string, command: string, extra: Record<string, unknown> = {}) =>
  ({ tool: 'Bash', tool_use_id: id, command, ...extra }) as any

// ---- activity ---------------------------------------------------------------

test('a main-loop tool call reports its activity, then none once it resolves', async ($, on) => {
  const g = gates()
  const h = setup(on, { toolCall: g.toolCall })
  await start($, h)
  const call = $.tool.call(bash('u1', 'git status --short'))
  await h.settle()
  expect(named(h, 'activity')).toEqual([{ type: 'event', name: 'activity', tool: 'Bash', summary: 'git' }])
  g.release('Bash')
  await call
  await advance(h, ACTIVITY_MIN_INTERVAL_MS)
  expect(named(h, 'activity')).toEqual([
    { type: 'event', name: 'activity', tool: 'Bash', summary: 'git' },
    { type: 'event', name: 'activity' },
  ])
})

test('activity is latest-wins: an unsent report is replaced, at most one a second', async ($, on) => {
  const g = gates()
  const h = setup(on, { toolCall: g.toolCall })
  await start($, h)
  const first = $.tool.call(bash('u1', 'ls'))
  await h.settle()
  g.release('Bash')
  await first
  $.tool.call({ tool: 'Read', tool_use_id: 'u2', file_path: '/Users/me/a.go' } as any)
  $.tool.call({ tool: 'Grep', tool_use_id: 'u3', pattern: 'TODO' } as any)
  await h.settle()
  expect(named(h, 'activity').length).toBe(1)
  await advance(h, ACTIVITY_MIN_INTERVAL_MS - 1)
  expect(named(h, 'activity').length).toBe(1)
  await advance(h, 1)
  expect(named(h, 'activity')).toEqual([
    { type: 'event', name: 'activity', tool: 'Bash', summary: 'ls' },
    { type: 'event', name: 'activity', tool: 'Grep', summary: 'TODO' },
  ])
})

test('with calls in parallel, activity falls back to the one still running', async ($, on) => {
  const g = gates()
  const h = setup(on, { toolCall: g.toolCall })
  await start($, h)
  $.tool.call(bash('u1', 'make test'))
  await h.settle()
  const second = $.tool.call({ tool: 'Read', tool_use_id: 'u2', file_path: '/tmp/x' } as any)
  await advance(h, ACTIVITY_MIN_INTERVAL_MS)
  g.release('Read')
  await second
  await advance(h, ACTIVITY_MIN_INTERVAL_MS)
  expect(named(h, 'activity').map((r) => r.tool)).toEqual(['Bash', 'Read', 'Bash'])
})

test('an activity report is sent once, never retried', async ($, on) => {
  const g = gates()
  // The hello lands; every report after it fails.
  const h = setup(on, { toolCall: g.toolCall, reportExit: (call) => (call === 1 ? 0 : 1) })
  await start($, h)
  $.tool.call(bash('u1', 'ls'))
  await advance(h, 10_000)
  expect(attempted(h, 'activity').length).toBe(1)
})

test('a subagent tool call reports no activity', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.tool.call(bash('u1', 'ls', { agentId: 'sub-1' }))
  await advance(h, ACTIVITY_MIN_INTERVAL_MS)
  expect(named(h, 'activity')).toEqual([])
})

test('with the bridge off, nothing is reported', async ($, on) => {
  const h = setup(on, { env: { LEO_BRIDGE_AGENT: AGENT } })
  await start($, h)
  await $.tool.call(bash('u1', 'ls'))
  await advance(h, ACTIVITY_MIN_INTERVAL_MS)
  expect(h.attempts).toEqual([])
})

// ---- attention --------------------------------------------------------------

const needsInput = (kind: string, extra: Record<string, unknown> = {}) => ({
  type: 'event',
  name: 'attention',
  state: 'needs_input',
  kind,
  ...extra,
})
const cleared = { type: 'event', name: 'attention', state: 'cleared' }

test('a clear with nothing pending sends nothing', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.turn.start({ text: 'go', turnId: 't1' })
  await $.turn.complete({ turnId: 't1', answer: '', durationMs: 1, isAborted: false, reason: 'answer' })
  await h.settle()
  expect(named(h, 'attention')).toEqual([])
})

test('AskUserQuestion needs input while the question is open', async ($, on) => {
  const g = gates()
  const h = setup(on, { toolCall: g.toolCall })
  await start($, h)
  const call = $.tool.call({ tool: 'AskUserQuestion', tool_use_id: 'q1', questions: [{ question: 'private?' }] } as any)
  await h.settle()
  expect(named(h, 'attention')).toEqual([needsInput('question', { tool: 'AskUserQuestion', summary: 'private?' })])
  g.release('AskUserQuestion')
  await call
  await h.settle()
  expect(named(h, 'attention')).toEqual([needsInput('question', { tool: 'AskUserQuestion', summary: 'private?' }), cleared])
})

async function askedAttention($: any, on: any, questions: unknown) {
  const g = gates()
  const h = setup(on, { toolCall: g.toolCall })
  await start($, h)
  $.tool.call({ tool: 'AskUserQuestion', tool_use_id: 'q1', questions } as any)
  await h.settle()
  return named(h, 'attention')
}

test('an AskUserQuestion with several questions shows the first and counts the rest', async ($, on) => {
  const got = await askedAttention($, on, [{ question: 'Which branch?' }, { question: 'Run tests?' }, { question: 'Tag it?' }])
  expect(got).toEqual([needsInput('question', { tool: 'AskUserQuestion', summary: 'Which branch? (+2 more)' })])
})

test('a long question is shortened so the remaining-count suffix survives the daemon clamp', async ($, on) => {
  const got = await askedAttention($, on, [{ question: 'x'.repeat(300) }, { question: 'y' }])
  const summary = got[0].summary as string
  expect(summary.length).toBeLessThanOrEqual(120)
  expect(summary.endsWith('… (+1 more)')).toBe(true)
})

test('a long question is clipped by code points, never through a surrogate pair', async ($, on) => {
  // 108 + 1 emoji fill the 109 code points left beside the ellipsis and suffix
  const got = await askedAttention($, on, [{ question: 'a'.repeat(108) + '😀😀😀😀' }, { question: 'y' }])
  expect(got[0].summary).toBe('a'.repeat(108) + '😀… (+1 more)')
})

test('question text is whitespace-folded, and a question with no usable text reports none', async ($, on) => {
  const folded = await askedAttention($, on, [{ question: '  Deploy\n  now?  ' }])
  expect(folded[0].summary).toBe('Deploy now?')
})

test('an AskUserQuestion without readable questions still needs input, with no summary', async ($, on) => {
  const got = await askedAttention($, on, [{ header: 'no question key' }, 7, null])
  expect(got).toEqual([needsInput('question', { tool: 'AskUserQuestion' })])
})

// ---- compaction -------------------------------------------------------------

const MESSAGES = [{ role: 'user', text: 'hi', toolUses: [] }]

const compact = (phase: string, trigger: string, extra: Record<string, unknown> = {}) => ({
  type: 'event',
  name: 'compact',
  phase,
  trigger,
  ...extra,
})

// The engine skips the calling plugin's own session.compact hook, so a leo
// compact command reports its phases itself. Compactions Claude runs itself
// are the PreCompact/PostCompact command hooks' to report: the mod cannot
// hear session.compact once cc-plugin-sec-default passes it over.
test('a compact command from leo reports manual phases', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed] })
  await start($, h)
  feed.line({ id: 'c1', op: 'compact' })
  await h.settle()
  expect(named(h, 'compact')).toEqual([compact('started', 'manual'), compact('completed', 'manual')])
})


test('a compaction the engine runs reports nothing from the mod', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.session.compact({ trigger: 'auto', messages: MESSAGES } as any)
  await h.settle()
  expect(named(h, 'compact')).toEqual([])
})

// Subagents are the SubagentStart/SubagentStop command hooks' to count; a
// hello that said 0 would reset the daemon's count on every reconnect.
test('a reconnect hello says nothing of subagents', { timeoutMs: 20_000 }, async ($, on) => {
  const feeds = [new Feed(), new Feed()]
  const h = setup(on, { feeds })
  await start($, h)
  feeds[0]!.end()
  await advance(h, 1000)
  const hellos = h.reports.filter((r: any) => r.type === 'hello')
  expect(hellos.length).toBe(2)
  expect(hellos.filter((r: any) => 'subagents' in r)).toEqual([])
})

// ---- usage telemetry --------------------------------------------------------

async function completeTurn($: any, h: any, turnId = 't1') {
  await $.turn.start({ text: 'go', turnId })
  await $.turn.complete({ turnId, answer: 'done', durationMs: 1, isAborted: false, reason: 'answer' })
  await h.settle()
}

test('turn.complete asks the host for the summary breakdown', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await completeTurn($, h)
  expect(h.usageCalls.length).toBe(1)
  expect(JSON.stringify(h.usageCalls[0])).toContain('"breakdown":"summary"')
})

test('turn.complete usage carries the session model id', async ($, on) => {
  const h = setup(on, { model: () => 'claude-sonnet-5-5' })
  await start($, h)
  await completeTurn($, h)
  const usage = named(h, 'turn.complete')[0]!.usage as Record<string, unknown>
  expect(usage.model).toBe('claude-sonnet-5-5')
  expect(usage.cost).toEqual({ usd: 0.01 })
})

test('a model lookup that fails still reports the usage', async ($, on) => {
  const h = setup(on, {
    model: () => {
      throw new Error('no model')
    },
  })
  await start($, h)
  await completeTurn($, h)
  const usage = named(h, 'turn.complete')[0]!.usage as Record<string, unknown>
  expect(usage.cost).toEqual({ usd: 0.01 })
  expect('model' in usage).toBe(false)
})

test('turn.complete is still sent when reading usage throws', async ($, on) => {
  const h = setup(on, {
    usage: () => {
      throw new Error('usage unavailable')
    },
  })
  await start($, h)
  await completeTurn($, h)
  const complete = named(h, 'turn.complete')
  expect(complete.length).toBe(1)
  expect('usage' in complete[0]!).toBe(false)
  expect(h.logs.some((l) => l.includes('reading session usage failed'))).toBe(true)
})

test('effort is reported again after the bridge reconnects', { timeoutMs: 20_000 }, async ($, on) => {
  const feeds = [new Feed(), new Feed()]
  const h = setup(on, { feeds })
  on('turn.step', async function* (_$: any, e: any) {
    return { turnId: e.turnId, index: e.index, answer: '', toolUses: [], stopReason: 'end_turn', usage: null }
  })
  const step = async (index: number) => {
    for await (const _ of $.turn.step({ turnId: 't1', index, model: 'claude-opus-5-5', messageCount: 1, effort: 'high' })) {
      // drain
    }
    await h.settle()
  }
  await start($, h)
  await step(0)
  await step(1)
  expect(named(h, 'effort').length).toBe(1)
  feeds[0]!.end()
  await advance(h, 1000)
  expect(h.spawns.length).toBe(2)
  // The daemon forgets effort across a reconnect (a restarted daemon, a new
  // launch): the same level must be said again.
  await step(2)
  expect(named(h, 'effort').length).toBe(2)
})
