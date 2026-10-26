import { expect, test } from 'claude-code/testing'
import { register } from '../hooks/register.js'

// What cc-plugin-sec-default passes over once it is seated (managed
// prependPlugins naming it, managed settings, a managed MCP file, or a
// team/enterprise account): a mod that subscribes to these never hears them,
// and Claude Code posts a "hook was skipped" notice per event. Everything the
// bridge needs from them comes from Claude command hooks instead.
// docs/specs/2026-10-26-bridge-command-hooks.md, "Problem".
const PASSED_OVER_EXACT = [
  'prompt.section',
  'prompt.context',
  'prompt.attachment',
  'prompt.compose',
  'skill.prompt',
  'attribution.text',
  'session.compact',
  'settings.read',
]
const PASSED_OVER_PREFIX = 'classic.'
// It also passes over the $.session.append doors attachment, hook-context and
// compaction; delegation.test.ts checks the mod appends nothing.

const isPassedOver = (name: string) => name.startsWith(PASSED_OVER_PREFIX) || PASSED_OVER_EXACT.includes(name)

function registeredEvents(): string[] {
  const names: string[] = []
  // register() chains .catch(...) on some handlers, as the real `on` allows.
  register((name: string) => {
    names.push(name)
    return { catch: () => undefined }
  })
  return names
}

test('the mod registers for no event the plugin guard passes over', () => {
  const events = registeredEvents()
  expect(events.length).toBeGreaterThan(0)
  expect(events.filter(isPassedOver)).toEqual([])
})

test('the mod keeps the engine events it reports from', () => {
  const events = registeredEvents()
  for (const name of ['session.start', 'prompt.submit', 'turn.start', 'turn.complete', 'turn.step', 'tool.call', 'session.end']) {
    expect(events).toContain(name)
  }
})
