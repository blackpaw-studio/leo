// Test harness for the leo-bridge mod: stubs every mods API call the mod
// makes and exposes what it did (reports sent, prompts submitted, ...).
import { mock } from 'claude-code/testing'

export const BIN = '/opt/leo/bin/leo'
export const AGENT = 'worker'
export const LAUNCH = 'launch-1'
export const HOME = '/Users/me'
export const ENV = { LEO_BRIDGE_BIN: BIN, LEO_BRIDGE_AGENT: AGENT, LEO_BRIDGE_LAUNCH: LAUNCH, HOME }
// What `leo bridge` and `leo bridge report` are spawned with: the key and the
// launch, so the daemon can refuse a launch that is not the key's current one.
export const STREAM_ARGV = [BIN, 'bridge', '--agent', AGENT, '--launch', LAUNCH]
export const REPORT_ARGV = [BIN, 'bridge', 'report', '--agent', AGENT, '--launch', LAUNCH]

type Chunk = { stream: 'stdout' | 'stderr'; text: string }

// A controllable child-process output stream. Each `$.process.spawn` the mod
// makes takes the next Feed; the test pushes text and ends it.
// The end of a child's output, with the code it exited with.
type End = { exit: number }

export class Feed {
  private items: Array<Chunk | End> = []
  private wake: (() => void) | null = null
  ended = false

  push(text: string, stream: 'stdout' | 'stderr' = 'stdout'): void {
    this.items = [...this.items, { stream, text }]
    this.notify()
  }

  line(obj: unknown): void {
    this.push(JSON.stringify(obj) + '\n')
  }

  end(exit = 0): void {
    this.items = [...this.items, { exit }]
    this.notify()
  }

  private notify(): void {
    const w = this.wake
    this.wake = null
    if (w) w()
  }

  async *chunks(): AsyncGenerator<Chunk, { value: { code: number | null; signal: string | null } }> {
    for (;;) {
      if (this.items.length === 0) {
        await new Promise<void>((resolve) => {
          this.wake = resolve
        })
        continue
      }
      const [head, ...rest] = this.items
      this.items = rest
      if ('exit' in head) {
        this.ended = true
        return { value: { code: head.exit, signal: null } }
      }
      yield head
    }
  }
}

const CLASSIC_OBSERVED = [
  'classic.PermissionRequest',
  'classic.Elicitation',
  'classic.PostToolUse',
  'classic.PostToolUseFailure',
  'classic.PermissionDenied',
  'classic.SubagentStart',
  'classic.SubagentStop',
  'classic.Notification',
  'classic.Stop',
]

export type SubmitResponder = (e: { text: string; asUser?: boolean }) => unknown

export interface HarnessOptions {
  env?: Record<string, string>
  store?: Record<string, unknown>
  feeds?: Feed[]
  submit?: SubmitResponder
  compact?: (e: { instructions?: string }) => unknown
  abort?: (e: { turnId: string }) => unknown
  command?: (e: { command: string; args?: string }) => unknown
  // Exit code of `leo bridge report`: a number for every call, or a function
  // of the 1-based call count (throwing makes $.process.run reject).
  reportExit?: number | ((call: number) => number)
  usage?: () => unknown
  // Awaited before each $.store.set lands, so a test can hold one back.
  beforeStoreSet?: (key: string) => Promise<void> | void
  // Answers a tool call the plugin passes on (the engine running the tool).
  toolCall?: (e: Record<string, unknown>) => unknown
  // Awaited before each $.session.id answers, so a test can hold one back.
  beforeSessionId?: () => Promise<void> | void
}

export interface Harness {
  clock: ReturnType<typeof mock.clock>
  // Reports the daemon accepted (exit 0), and every attempt including failed ones.
  reports: Array<Record<string, unknown>>
  attempts: Array<Record<string, unknown>>
  reportArgv: string[][]
  reportStdin: Array<string | undefined>
  submits: Array<Record<string, unknown>>
  compacts: Array<Record<string, unknown>>
  aborts: Array<Record<string, unknown>>
  commands: Array<Record<string, unknown>>
  spawns: string[][]
  sessionId: string
  logs: string[]
  store: Map<string, unknown>
  feeds: Feed[]
  statuses: Array<string | undefined>
  toasts: string[]
  invalidates: string[]
  toolCalls: Array<Record<string, unknown>>
  settle: () => Promise<void>
}

export function setup(on: any, opts: HarnessOptions = {}): Harness {
  const clock = mock.clock(on, { now: 1_000_000 })
  mock.env(on, opts.env ?? ENV)
  const store = new Map<string, unknown>(Object.entries(opts.store ?? {}))
  const feeds = opts.feeds ?? [new Feed()]
  const h: Harness = {
    clock,
    reports: [],
    attempts: [],
    reportArgv: [],
    reportStdin: [],
    submits: [],
    compacts: [],
    aborts: [],
    commands: [],
    spawns: [],
    sessionId: 'sess-1',
    logs: [],
    store,
    feeds,
    statuses: [],
    toasts: [],
    invalidates: [],
    toolCalls: [],
    settle: flush,
  }

  on('session.start', () => ({ cwd: '/work' }))
  on('turn.start', ($: any, e: any) => ({ turnId: e.turnId }))
  on('turn.complete', () => ({ text: '' }))
  on('session.end', ($: any, e: any) => ({ sessionId: e.sessionId }))
  on('ui.log', ($: any, e: any) => {
    h.logs.push(e.text)
    return { value: undefined }
  })
  on('ui.status', ($: any, e: any) => {
    h.statuses.push(e.text)
    return { value: undefined }
  })
  on('ui.toast', ($: any, e: any) => {
    h.toasts.push(e.text)
    return { value: undefined }
  })
  on('ui.invalidate', ($: any, e: any) => {
    h.invalidates.push(e.event)
    return { value: undefined }
  })
  // The engine's own drawing of a component a test mounts.
  on('ui.render', ($: any, e: any) => {
    const { Text } = $.ui.resolve(e)
    return (globalThis as any).h(Text, null, 'engine band')
  })
  on('prompt.compose', () => ({ sections: [{ id: 'intro', text: 'core prompt', scope: 'shared' }] }))
  on('agent.offer', () => ({ isOffered: true }))
  // The classic events the mod observes: nothing beneath answers them.
  for (const name of CLASSIC_OBSERVED) on(name, () => ({}))
  on('tool.call', ($: any, e: any) => {
    h.toolCalls.push({ ...e })
    if (opts.toolCall) return opts.toolCall(e)
    return { result: 'ran', text: 'ran' }
  })
  on('store.get', ($: any, e: any) => ({ value: store.get(e.key) }))
  on('store.set', async ($: any, e: any) => {
    if (opts.beforeStoreSet) await opts.beforeStoreSet(e.key)
    store.set(e.key, e.value)
    return { value: undefined }
  })
  on('store.delete', ($: any, e: any) => {
    store.delete(e.key)
    return { value: undefined }
  })
  on('store.keys', () => ({ value: [...store.keys()] }))
  on('session.id', async () => {
    if (opts.beforeSessionId) await opts.beforeSessionId()
    return { value: h.sessionId }
  })
  on('session.version', () => ({ value: { version: '2.1.289', base: '2.1.289' } }))
  on('session.usage', () => {
    if (opts.usage) return { value: opts.usage() }
    return {
      value: {
        startedAt: 1,
        context: { tokens: 10, window: 200000, percent: 0 },
        rateLimits: [],
        cost: { usd: 0.01 },
      },
    }
  })
  let reportCalls = 0
  on('process.run', ($: any, e: any) => {
    reportCalls++
    const argv = [...e.argv]
    const exit = typeof opts.reportExit === 'function' ? opts.reportExit(reportCalls) : (opts.reportExit ?? 0)
    // The hook sees $.process.run(argv, init) as { argv, init }; the report
    // JSON is init.stdin.
    const stdin: string | undefined = e.init?.stdin
    h.reportArgv.push(argv)
    h.reportStdin.push(stdin)
    h.attempts.push(JSON.parse(stdin ?? 'null'))
    if (exit === 0) h.reports.push(JSON.parse(stdin ?? 'null'))
    return { value: { exitCode: exit, stdout: '', stderr: exit === 0 ? '' : 'daemon down' } }
  })
  on('process.spawn', async function* ($: any, e: any) {
    h.spawns.push([...e.argv])
    const feed = feeds[h.spawns.length - 1]
    if (!feed) {
      // No more scripted streams: block forever like a quiet child.
      await new Promise(() => {})
      return { value: { code: 0, signal: null } }
    }
    return yield* feed.chunks()
  })
  on('prompt.submit', async ($: any, e: any) => {
    h.submits.push({ ...e })
    if (opts.submit) return opts.submit(e)
    return { text: e.text }
  })
  on('session.compact', async ($: any, e: any) => {
    h.compacts.push({ ...e })
    if (opts.compact) return opts.compact(e)
    return { messages: [{ role: 'user', text: 'summary', toolUses: [] }] }
  })
  on('turn.abort', async ($: any, e: any) => {
    h.aborts.push({ ...e })
    if (opts.abort) return opts.abort(e)
    return { value: undefined }
  })
  on('command.run', async ($: any, e: any) => {
    h.commands.push({ ...e })
    if (opts.command) return opts.command(e)
    return {}
  })
  return h
}

// Lets queued promise work run to completion without moving the mock clock.
// (clock.settle() waits up to ~1 s for quiescence, which never comes while a
// bridge stream is open, so tests flush with real macrotask turns instead.)
const tick = () => new Promise<void>((resolve) => setTimeout(resolve, 0))
export async function flush(): Promise<void> {
  for (let i = 0; i < 20; i++) await tick()
}

// Moves the mock clock (each move costs ~1 s of real time) and flushes.
export async function advance(h: Harness, ms: number): Promise<void> {
  await flush()
  await h.clock.advance(ms)
  await flush()
}

// Fires session.start and lets the pump (started on a 0 ms timer) spawn its
// first stream and say hello.
export async function start($: any, h: Harness): Promise<void> {
  await $.session.start({ surface: 'terminal', isInteractive: true, cwd: '/work' })
  await advance(h, 0)
}

export function acks(h: Harness): Array<Record<string, unknown>> {
  return h.reports.filter((r) => r.type === 'ack')
}

export function events(h: Harness): Array<Record<string, unknown>> {
  return h.reports.filter((r) => r.type === 'event')
}
