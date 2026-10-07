import { expect, test } from 'claude-code/testing'
import { compactTrigger, MAX_SUMMARY_CHARS, toolActivity } from '../hooks/protocol.js'

const HOME = '/Users/me'

test('Bash summarizes as the first word of the command', () => {
  expect(toolActivity('Bash', { command: '  git push --force origin main' }, HOME)).toEqual({ tool: 'Bash', summary: 'git' })
})

test('Read, Edit and Write summarize as the home-abbreviated path', () => {
  expect(toolActivity('Read', { file_path: '/Users/me/src/a.go' }, HOME)).toEqual({ tool: 'Read', summary: '~/src/a.go' })
  expect(toolActivity('Edit', { file_path: '/etc/hosts', old_string: 'secret' }, HOME)).toEqual({ tool: 'Edit', summary: '/etc/hosts' })
  expect(toolActivity('Write', { file_path: '/Users/me', content: 'x' }, HOME)).toEqual({ tool: 'Write', summary: '~' })
})

test('a sibling of home is not abbreviated', () => {
  expect(toolActivity('Read', { file_path: '/Users/meg/a' }, HOME)).toEqual({ tool: 'Read', summary: '/Users/meg/a' })
})

test('paths stay whole without a home', () => {
  expect(toolActivity('Read', { file_path: '/Users/me/a' }, '')).toEqual({ tool: 'Read', summary: '/Users/me/a' })
})

test('Grep and Glob summarize as the pattern', () => {
  expect(toolActivity('Grep', { pattern: 'func main', path: '/x' }, HOME)).toEqual({ tool: 'Grep', summary: 'func main' })
  expect(toolActivity('Glob', { pattern: '**/*.go' }, HOME)).toEqual({ tool: 'Glob', summary: '**/*.go' })
})

test('WebFetch summarizes as the host, never credentials, path or query', () => {
  const fetch = (url: unknown) => toolActivity('WebFetch', { url, prompt: 'p' }, HOME)
  expect(fetch('https://user:pw@example.com:8443/a?token=x')).toEqual({ tool: 'WebFetch', summary: 'example.com' })
  expect(fetch('http://docs.example.org')).toEqual({ tool: 'WebFetch', summary: 'docs.example.org' })
  expect(fetch('not a url')).toEqual({ tool: 'WebFetch' })
})

test('MCP and other tools carry only the tool name', () => {
  expect(toolActivity('mcp__leo__leo_dispatch', { prompt: 'secret' }, HOME)).toEqual({ tool: 'mcp__leo__leo_dispatch' })
  expect(toolActivity('Agent', { prompt: 'secret', description: 'd' }, HOME)).toEqual({ tool: 'Agent' })
})

test('missing or non-string inputs give no summary', () => {
  expect(toolActivity('Bash', {}, HOME)).toEqual({ tool: 'Bash' })
  expect(toolActivity('Read', { file_path: 42 }, HOME)).toEqual({ tool: 'Read' })
  expect(toolActivity('Bash', { command: '   ' }, HOME)).toEqual({ tool: 'Bash' })
  expect(toolActivity('Grep', null, HOME)).toEqual({ tool: 'Grep' })
})

test('every field is capped', () => {
  const long = 'x'.repeat(5000)
  const got = toolActivity('Grep', { pattern: long }, HOME)
  expect(got.summary!.length).toBe(MAX_SUMMARY_CHARS)
  expect(toolActivity('mcp__' + long, {}, HOME).tool!.length).toBe(MAX_SUMMARY_CHARS)
})

test('compact triggers map to manual or auto; precompute is not reported', () => {
  expect(compactTrigger('manual')).toBe('manual')
  expect(compactTrigger('plugin')).toBe('manual')
  expect(compactTrigger('auto')).toBe('auto')
  expect(compactTrigger('precompute')).toBe(null)
})
