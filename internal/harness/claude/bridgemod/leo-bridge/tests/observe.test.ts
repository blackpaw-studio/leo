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
  await $.classic.PermissionRequest({ tool_name: 'Bash', tool_input: { command: 'ls' } })
  await $.classic.SubagentStart({ agent_id: 'a1', agent_type: 'general-purpose' })
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

test('a permission prompt needs input until the tool runs', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.classic.PermissionRequest({ tool_name: 'Bash', tool_input: { command: 'rm -rf build' } })
  await h.settle()
  expect(named(h, 'attention')).toEqual([needsInput('permission', { tool: 'Bash', summary: 'rm' })])
  await $.classic.PostToolUse({ tool_name: 'Bash', tool_input: {}, tool_response: 'ok', tool_use_id: 'u1' })
  await h.settle()
  expect(named(h, 'attention')).toEqual([needsInput('permission', { tool: 'Bash', summary: 'rm' }), cleared])
})

test('a failed or denied tool clears attention', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.classic.PermissionRequest({ tool_name: 'mcp__x__y', tool_input: { secret: 's' } })
  await $.classic.PostToolUseFailure({ tool_name: 'mcp__x__y', tool_input: {}, tool_use_id: 'u1', error: 'no' })
  await $.classic.PermissionRequest({ tool_name: 'Write', tool_input: { file_path: '/Users/me/a', content: 'secret' } })
  await $.classic.PermissionDenied({ tool_name: 'Write', tool_input: {}, tool_use_id: 'u2', reason: 'denied' })
  await h.settle()
  expect(named(h, 'attention')).toEqual([
    needsInput('permission', { tool: 'mcp__x__y' }),
    cleared,
    needsInput('permission', { tool: 'Write', summary: '~/a' }),
    cleared,
  ])
})

test('turn.complete clears attention', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.turn.start({ text: 'go', turnId: 't1' })
  await $.classic.PermissionRequest({ tool_name: 'Bash', tool_input: { command: 'ls' } })
  await $.turn.complete({ turnId: 't1', answer: '', durationMs: 1, isAborted: true, reason: 'aborted' })
  await h.settle()
  expect(named(h, 'attention')).toEqual([needsInput('permission', { tool: 'Bash', summary: 'ls' }), cleared])
})

test('a clear with nothing pending sends nothing', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.turn.start({ text: 'go', turnId: 't1' })
  await $.classic.PostToolUse({ tool_name: 'Bash', tool_input: {}, tool_response: 'ok', tool_use_id: 'u1' })
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
  expect(named(h, 'attention')).toEqual([needsInput('question', { tool: 'AskUserQuestion' })])
  g.release('AskUserQuestion')
  await call
  await $.classic.PostToolUse({ tool_name: 'AskUserQuestion', tool_input: {}, tool_response: 'a', tool_use_id: 'q1' })
  await h.settle()
  expect(named(h, 'attention')).toEqual([needsInput('question', { tool: 'AskUserQuestion' }), cleared])
})

test('an MCP elicitation needs input, naming the server', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.classic.Elicitation({ mcp_server_name: 'github', message: 'token please' })
  await h.settle()
  expect(named(h, 'attention')).toEqual([needsInput('elicitation', { summary: 'github' })])
})

test('a subagent permission prompt or tool result leaves attention alone', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.classic.PermissionRequest({ tool_name: 'Bash', tool_input: { command: 'ls' }, agent_id: 'sub-1' })
  await $.classic.PermissionRequest({ tool_name: 'Bash', tool_input: { command: 'ls' } })
  await $.classic.PostToolUse({ tool_name: 'Bash', tool_input: {}, tool_response: 'ok', tool_use_id: 'u1', agent_id: 'sub-1' })
  await h.settle()
  expect(named(h, 'attention')).toEqual([needsInput('permission', { tool: 'Bash', summary: 'ls' })])
})

test('an idle_prompt notification is not attention', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.classic.Notification({ message: 'Claude is waiting for your input', notification_type: 'idle_prompt' } as any)
  await h.settle()
  expect(named(h, 'attention')).toEqual([])
})

// ---- subagents --------------------------------------------------------------

test('subagents are counted by agent_id and survive turn.complete', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.turn.start({ text: 'go', turnId: 't1' })
  await $.classic.SubagentStart({ agent_id: 'a1', agent_type: 'general-purpose' })
  await $.classic.SubagentStart({ agent_id: 'a2', agent_type: 'Explore' })
  await $.classic.SubagentStart({ agent_id: 'a2', agent_type: 'Explore' })
  await $.turn.complete({ turnId: 't1', answer: '', durationMs: 1, isAborted: false, reason: 'answer' })
  await h.settle()
  expect(named(h, 'subagents').map((r) => r.running)).toEqual([1, 2])
  const stop = (id: string) =>
    $.classic.SubagentStop({ agent_id: id, agent_type: 'x', stop_hook_active: false, agent_transcript_path: '/t' })
  await stop('a1')
  await stop('unknown')
  await stop('a2')
  await h.settle()
  expect(named(h, 'subagents')).toEqual([
    { type: 'event', name: 'subagents', running: 1 },
    { type: 'event', name: 'subagents', running: 2 },
    { type: 'event', name: 'subagents', running: 1 },
    { type: 'event', name: 'subagents', running: 0 },
  ])
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

test('a main-loop compaction reports started, then completed', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.session.compact({ trigger: 'auto', messages: MESSAGES } as any)
  await h.settle()
  expect(named(h, 'compact')).toEqual([compact('started', 'auto'), compact('completed', 'auto')])
})

test('a skipped compaction reports failed with why', async ($, on) => {
  const h = setup(on, { compact: () => ({ skip: 'nothing to compact' }) })
  await start($, h)
  await $.session.compact({ trigger: 'manual', messages: MESSAGES } as any)
  await h.settle()
  expect(named(h, 'compact')).toEqual([
    compact('started', 'manual'),
    compact('failed', 'manual', { error: 'nothing to compact' }),
  ])
})

test('a compaction that throws reports failed', async ($, on) => {
  const h = setup(on, {
    compact: () => {
      throw new Error('prompt too long')
    },
  })
  await start($, h)
  await expect($.session.compact({ trigger: 'auto', messages: MESSAGES } as any)).rejects.toThrow()
  await h.settle()
  // The harness surfaces a throwing engine as its own error text.
  const reports = named(h, 'compact')
  expect(reports.length).toBe(2)
  expect(reports[0]).toEqual(compact('started', 'auto'))
  expect(reports[1]).toMatchObject({ phase: 'failed', trigger: 'auto' })
  expect(typeof reports[1]!.error).toBe('string')
})

test('subagent and precompute compactions are not reported', async ($, on) => {
  const h = setup(on)
  await start($, h)
  await $.session.compact({ trigger: 'auto', agentId: 'sub-1', messages: MESSAGES } as any)
  await $.session.compact({ trigger: 'precompute', messages: MESSAGES } as any)
  await h.settle()
  expect(named(h, 'compact')).toEqual([])
})

// The engine skips the calling plugin's own session.compact hook, so a leo
// compact command reports its phases itself.
test('a compact command from leo reports manual phases', async ($, on) => {
  const feed = new Feed()
  const h = setup(on, { feeds: [feed] })
  await start($, h)
  feed.line({ id: 'c1', op: 'compact' })
  await h.settle()
  expect(named(h, 'compact')).toEqual([compact('started', 'manual'), compact('completed', 'manual')])
})

// Every reconnect says hello; it carries the subagents still running so the
// daemon keeps holding the agent instead of reading the hello as a reset.
test('a reconnect hello carries the running subagent count', { timeoutMs: 20_000 }, async ($, on) => {
  const feeds = [new Feed(), new Feed()]
  const h = setup(on, { feeds })
  await start($, h)
  await $.classic.SubagentStart({ agent_id: 'a1', agent_type: 'general-purpose' })
  await $.classic.SubagentStart({ agent_id: 'a2', agent_type: 'general-purpose' })
  await $.classic.SubagentStop({ agent_id: 'a2', agent_type: 'general-purpose' })
  await h.settle()
  feeds[0]!.end()
  await advance(h, 1000)
  const hellos = h.reports.filter((r: any) => r.type === 'hello')
  expect(hellos.map((r: any) => r.subagents)).toEqual([0, 1])
})
