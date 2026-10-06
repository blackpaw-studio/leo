import { expect, test } from 'claude-code/testing'
import { delegationNote, formatElapsed, isAgentHidden, parseState, parseStateLine, rosterRowText, statusLine } from '../hooks/roster.js'

test('parseState coerces fields and drops dispatches without an id', () => {
  const state = parseState({
    op: 'state',
    delegation: { enabled: true, section: 's', hide_agents: ['Explore', 7] },
    dispatches: [{ id: 'd1', status: 'running', active_seconds: 'x', tokens_in: 5 }, { status: 'idle' }, 'junk'],
  })
  expect(state).toEqual({
    delegation: { enabled: true, section: 's', hideAgents: ['Explore'] },
    dispatches: [{ id: 'd1', name: '', role: '', template: '', model: '', status: 'running', stalled: false, activeSeconds: 0, tokensIn: 5, tokensOut: undefined, costUsd: undefined }],
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

test('a row falls back from name to role to template to id, and omits unknown cost', () => {
  const d = { id: 'd9', name: '', role: '', template: 'codex', model: '', status: 'queued', stalled: false, activeSeconds: 0, tokensIn: undefined, tokensOut: undefined, costUsd: undefined }
  expect(rosterRowText(d, 0, 0)).toBe('… codex  queued  0:00  –/–')
})
