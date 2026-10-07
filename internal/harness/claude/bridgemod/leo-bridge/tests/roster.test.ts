import { expect, test } from 'claude-code/testing'
import { bandHeader, delegationNote, formatElapsed, isAgentHidden, parseState, parseStateLine, rosterRows, rowText, shownDispatches, statusLine, trackTerminal } from '../hooks/roster.js'

test('parseState coerces fields and drops dispatches without an id', () => {
  const state = parseState({
    op: 'state',
    delegation: { enabled: true, section: 's', hide_agents: ['Explore', 7] },
    dispatches: [{ id: 'd1', status: 'running', active_seconds: 'x', tokens_in: 5, effort: 7 }, { status: 'idle' }, 'junk'],
  })
  expect(state).toEqual({
    delegation: { enabled: true, section: 's', hideAgents: ['Explore'] },
    dispatches: [{ id: 'd1', name: '', role: '', template: '', model: '', effort: '', status: 'running', stalled: false, activeSeconds: 0, tokensIn: 5, tokensOut: undefined, costUsd: undefined }],
  })
})

test('parseStateLine leaves commands and garbage to the command path', () => {
  expect(parseStateLine('{"id":"c1","op":"deliver","text":"x"}')).toBeNull()
  expect(parseStateLine('not json')).toBeNull()
  expect(parseStateLine('{"op":"state"}')).toEqual({ delegation: { enabled: false, section: '', hideAgents: [] }, dispatches: [] })
})

test('the note carries the new section, or says delegation is off', () => {
  expect(delegationNote({ enabled: true, section: 'roles: implement' })).toContain('roles: implement')
  expect(delegationNote({ enabled: false, section: 'stale' })).toContain('delegation is now OFF')
  expect(delegationNote({ enabled: false, section: 'stale' })).not.toContain('stale')
})

test('a fallback lets every agent through', () => {
  const state = { delegation: { enabled: true, hideAgents: ['Explore'] } }
  expect(isAgentHidden(state, false, 'Explore', false)).toBe(true)
  expect(isAgentHidden(state, true, 'Explore', true)).toBe(false)
  expect(isAgentHidden(null, false, '', true)).toBe(false)
})

test('the status line names the fallback only while delegation could be on', () => {
  expect(statusLine(null, null)).toBeUndefined()
  expect(statusLine(null, 'no state from leo')).toBe('native agents allowed: no state from leo')
  expect(statusLine({ delegation: { enabled: false }, dispatches: [] }, 'leo bridge down')).toBeUndefined()
})

test('elapsed time reads m:ss, then h:mm:ss', () => {
  expect(formatElapsed(5)).toBe('0:05')
  expect(formatElapsed(3725)).toBe('1:02:05')
})

const row = (over: object) => ({ id: 'd', name: '', role: '', template: '', model: '', effort: '', status: 'running', stalled: false, activeSeconds: 0, tokensIn: undefined, tokensOut: undefined, costUsd: undefined, ...over })

test('the status line says nothing of dispatches', () => {
  const state = { delegation: { enabled: true }, dispatches: [row({ status: 'running' }), row({ status: 'idle' })] }
  expect(statusLine(state, null)).toBeUndefined()
  expect(statusLine(state, 'leo_dispatch failed')).toBe('native agents allowed: leo_dispatch failed')
})

test('parseState reads effort as a string', () => {
  expect(parseState({ op: 'state', dispatches: [{ id: 'd1', effort: 'high' }] })?.dispatches[0].effort).toBe('high')
})

test('rows are indented and padded into columns', () => {
  const rows = rosterRows(
    [
      row({ id: 'a', name: 'impl-auth', model: 'opus', effort: 'high', activeSeconds: 250, tokensIn: 182_000, tokensOut: 12_400, costUsd: 1.92 }),
      row({ id: 'b', name: 'reviewer', model: 'sonnet', effort: 'med', status: 'idle', activeSeconds: 83, tokensIn: 41_000, tokensOut: 3100, costUsd: 0.31 }),
    ],
    0,
    0,
  )
  expect(rows.map(rowText)).toEqual([
    '    ⟳ impl-auth  opus · high   4:10  182.0k/12.4k  $1.92',
    '    ⏸ reviewer   sonnet · med  1:23    41.0k/3.1k  $0.31',
  ])
  expect(rows.map((r) => r.isLive)).toEqual([true, true])
})

test('a row falls back from name to role to template to id, omits empty effort and unknown cost', () => {
  const rows = rosterRows([row({ id: 'd9', template: 'codex', model: 'haiku', status: 'queued' })], 0, 0)
  expect(rowText(rows[0])).toBe('    … codex  haiku  0:00  –/–')
})

test('a long label is cut to 16 columns', () => {
  expect(rowText(rosterRows([row({ name: 'a-very-long-dispatch-name' })], 0, 0)[0])).toBe('    ⟳ a-very-long-disp  0:00  –/–')
})

test('a stalled row is marked after its elapsed time and its glyph is yellow', () => {
  const rows = rosterRows([row({ id: 'a', name: 'x', stalled: true }), row({ id: 'b', name: 'y' })], 0, 0)
  expect(rows.map(rowText)).toEqual(['    ⟳ x  0:00 stalled  –/–', '    ⟳ y  0:00          –/–'])
  expect(rows[0].segments.find((s) => s.text === '⟳')?.color).toBe('warning')
})

test('the glyph carries the status color; model, tokens and cost are dim', () => {
  const [done, failed, idle] = rosterRows([row({ status: 'done', model: 'm', costUsd: 1 }), row({ status: 'failed' }), row({ status: 'idle' })], 0, 0)
  expect(done.segments[1]).toMatchObject({ text: '✓', color: 'success' })
  expect(failed.segments[1]).toMatchObject({ text: '✗', color: 'error' })
  expect(idle.segments[1]).toMatchObject({ text: '⏸', dimColor: true })
  expect(done.isLive).toBe(false)
  expect(done.segments.filter((s) => s.dimColor).map((s) => s.text.trim())).toEqual(['m', '–/–', '$1.00'])
})

test('the header is padded 2 and its rule runs to the band width, or 40 when unknown', () => {
  expect(bandHeader(30)).toBe('  leo dispatches ' + '─'.repeat(13))
  expect(bandHeader(undefined)).toBe('  leo dispatches ' + '─'.repeat(23))
})

test('a terminal dispatch shows for 10 s after first seen terminal; one terminal at load never shows', () => {
  const done = row({ id: 'a', status: 'done' })
  const live = row({ id: 'b' })
  const atLoad = trackTerminal(null, [done, live], 1000)
  expect(shownDispatches([done, live], atLoad, 1000).map((d) => d.id)).toEqual(['b'])
  const later = trackTerminal(new Map(), [done], 1000)
  expect(trackTerminal(later, [done], 5000).get('a')).toBe(1000)
  expect(shownDispatches([done], later, 10_999).map((d) => d.id)).toEqual(['a'])
  expect(shownDispatches([done], later, 11_000)).toEqual([])
})
