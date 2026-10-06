import { expect, test } from 'claude-code/testing'
import {
  ackedFromStore,
  ackReport,
  appendAcked,
  describeExit,
  eventReport,
  helloReport,
  nextBackoff,
  parseCommand,
  REPORT_RETRY_DELAYS_MS,
  splitLines,
  touchedEntry,
} from '../hooks/protocol.js'

// A report the daemon does not take (down, restarting) is retried with
// backoff for about a minute before it is dropped: long enough to ride out a
// daemon restart.
test('report retries back off and span about a minute', () => {
  const total = REPORT_RETRY_DELAYS_MS.reduce((sum, ms) => sum + ms, 0)
  expect(total).toBeGreaterThanOrEqual(55_000)
  expect(total).toBeLessThanOrEqual(65_000)
  REPORT_RETRY_DELAYS_MS.slice(1).forEach((ms, i) => expect(ms).toBeGreaterThanOrEqual(REPORT_RETRY_DELAYS_MS[i]!))
})

test('splitLines carries a partial line across chunks', () => {
  const a = splitLines('', '{"id":"1"')
  expect(a).toEqual({ lines: [], carry: '{"id":"1"' })
  const b = splitLines(a.carry, '}\n{"id":"2"}\n{"id"')
  expect(b).toEqual({ lines: ['{"id":"1"}', '{"id":"2"}'], carry: '{"id"' })
})

test('splitLines drops blank lines and CRs', () => {
  expect(splitLines('', 'a\r\n\n  \nb\n')).toEqual({ lines: ['a', 'b'], carry: '' })
})

test('parseCommand maps each op', () => {
  expect(parseCommand('{"id":"d","op":"deliver","text":"hi","as_user":true}')).toEqual({
    kind: 'command',
    command: { id: 'd', op: 'deliver', text: 'hi', asUser: true },
  })
  expect(parseCommand('{"id":"d","op":"deliver","text":"hi"}')).toEqual({
    kind: 'command',
    command: { id: 'd', op: 'deliver', text: 'hi', asUser: false },
  })
  expect(parseCommand('{"id":"k","op":"compact","instructions":"x"}')).toEqual({
    kind: 'command',
    command: { id: 'k', op: 'compact', instructions: 'x' },
  })
  expect(parseCommand('{"id":"c","op":"clear"}')).toEqual({ kind: 'command', command: { id: 'c', op: 'clear' } })
  expect(parseCommand('{"id":"i","op":"interrupt"}')).toEqual({
    kind: 'command',
    command: { id: 'i', op: 'interrupt' },
  })
})

test('parseCommand rejects garbage and bad payloads', () => {
  expect(parseCommand('nope').kind).toBe('garbage')
  expect(parseCommand('[1]').kind).toBe('garbage')
  expect(parseCommand('{"op":"clear"}').kind).toBe('garbage')
  expect(parseCommand('{"id":7,"op":"clear"}').kind).toBe('garbage')
  expect(parseCommand('{"id":"x","op":"fly"}')).toEqual({ kind: 'invalid', id: 'x', error: 'unknown op: fly' })
  expect(parseCommand('{"id":"x","op":"compact","instructions":3}')).toEqual({
    kind: 'invalid',
    id: 'x',
    error: 'compact: instructions must be a string',
  })
})

test('appendAcked caps, dedups, and never mutates its input', () => {
  const list = ['a', 'b', 'c']
  expect(appendAcked(list, 'd', 3)).toEqual(['b', 'c', 'd'])
  expect(appendAcked(list, 'a', 3)).toEqual(['b', 'c', 'a'])
  expect(list).toEqual(['a', 'b', 'c'])
})

test('ackedFromStore tolerates junk', () => {
  expect(ackedFromStore(undefined)).toEqual([])
  expect(ackedFromStore({ a: 1 })).toEqual([])
  expect(ackedFromStore(['a', 2, 'b'])).toEqual(['a', 'b'])
})

// A daemon restart cuts the stream; once the daemon is back the mod must be
// on it within a few seconds, so the backoff stays short.
test('nextBackoff doubles to a 5s cap and resets after a 60s stream', () => {
  const waits: number[] = []
  let backoff = 1000
  for (let i = 0; i < 6; i++) {
    const plan = nextBackoff(backoff, 10)
    waits.push(plan.waitMs)
    backoff = plan.next
  }
  expect(waits).toEqual([1000, 2000, 4000, 5000, 5000, 5000])
  expect(nextBackoff(5000, 60_001)).toEqual({ waitMs: 1000, next: 2000 })
  expect(nextBackoff(5000, 60_000).waitMs).toBe(5000)
})

test('touchedEntry restamps an entry and keeps what it holds', () => {
  const entry = { ids: ['a'], at: 1, inflight: { launch: 'l', ids: ['b'] } }
  expect(touchedEntry(entry, 9)).toEqual({ ids: ['a'], at: 9, inflight: { launch: 'l', ids: ['b'] } })
  expect(touchedEntry(['a'], 9)).toEqual({ ids: ['a'], at: 9 })
  expect(touchedEntry(undefined, 9)).toEqual({ ids: [], at: 9 })
})

test('describeExit names the exit code or signal and keeps stderr', () => {
  expect(describeExit({ code: 1, signal: null }, ' daemon down \n')).toBe('exit 1: daemon down')
  expect(describeExit({ code: null, signal: 'SIGTERM' }, '')).toBe('signal SIGTERM')
  expect(describeExit({ code: 0, signal: null }, '')).toBe('exit 0')
  expect(describeExit(undefined, 'x')).toBe('x')
})

test('report shapes', () => {
  expect(helloReport('s', '2.1.289', true)).toEqual({ type: 'hello', session_id: 's', claude_version: '2.1.289', busy: true })
  expect(helloReport('s', '2.1.289', undefined)).toEqual({ type: 'hello', session_id: 's', claude_version: '2.1.289' })
  expect(ackReport('a', true)).toEqual({ type: 'ack', id: 'a', ok: true })
  expect(ackReport('a', true, 'ignored')).toEqual({ type: 'ack', id: 'a', ok: true })
  expect(ackReport('a', false, 'why')).toEqual({ type: 'ack', id: 'a', ok: false, error: 'why' })
  expect(eventReport('turn.start')).toEqual({ type: 'event', name: 'turn.start' })
  expect(eventReport('session.end', { reason: 'clear' })).toEqual({ type: 'event', name: 'session.end', reason: 'clear' })
})
