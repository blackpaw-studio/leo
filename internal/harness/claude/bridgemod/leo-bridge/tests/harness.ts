// Test harness for the leo-bridge mod: stubs every mods API call the mod
// makes and exposes what it did (reports sent, prompts submitted, ...).
import { mock } from 'claude-code/testing'

export const BIN = '/opt/leo/bin/leo'
export const AGENT = 'worker'
export const ENV = { LEO_BRIDGE_BIN: BIN, LEO_PROCESS_NAME: AGENT }

type Chunk = { stream: 'stdout' | 'stderr'; text: string }

// A controllable child-process output stream. Each `$.process.spawn` the mod
// makes takes the next Feed; the test pushes text and ends it.
export class Feed {
  private items: Array<Chunk | null> = []
  private wake: (() => void) | null = null
  ended = false

  push(text: string, stream: 'stdout' | 'stderr' = 'stdout'): void {
    this.items = [...this.items, { stream, text }]
    this.notify()
  }

  line(obj: unknown): void {
    this.push(JSON.stringify(obj) + '\n')
  }

  end(): void {
    this.items = [...this.items, null]
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
      if (head === null) {
        this.ended = true
        return { value: { code: 0, signal: null } }
      }
      yield head
    }
  }
}

export type SubmitResponder = (e: { text: string; asUser?: boolean }) => unknown

export interface HarnessOptions {
  env?: Record<string, string>
  store?: Record<string, unknown>
  feeds?: Feed[]
  submit?: SubmitResponder
  compact?: (e: { instructions?: string }) => unknown
  abort?: (e: { turnId: string }) => unknown
  command?: (e: { command: string; args?: string }) => unknown
  reportExit?: number
}

export interface Harness {
  clock: ReturnType<typeof mock.clock>
  reports: Array<Record<string, unknown>>
  reportArgv: string[][]
  submits: Array<Record<string, unknown>>
  compacts: Array<Record<string, unknown>>
  aborts: Array<Record<string, unknown>>
  commands: Array<Record<string, unknown>>
  spawns: string[][]
  logs: string[]
  store: Map<string, unknown>
  feeds: Feed[]
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
    reportArgv: [],
    submits: [],
    compacts: [],
    aborts: [],
    commands: [],
    spawns: [],
    logs: [],
    store,
    feeds,
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
  on('store.get', ($: any, e: any) => ({ value: store.get(e.key) }))
  on('store.set', ($: any, e: any) => {
    store.set(e.key, e.value)
    return { value: undefined }
  })
  on('session.id', () => ({ value: 'sess-1' }))
  on('session.version', () => ({ value: { version: '2.1.289', base: '2.1.289' } }))
  on('session.usage', () => ({
    value: {
      startedAt: 1,
      context: { tokens: 10, window: 200000, percent: 0 },
      rateLimits: [],
      cost: { usd: 0.01 },
    },
  }))
  on('process.run', ($: any, e: any) => {
    const argv = [...e.argv]
    h.reportArgv.push(argv)
    h.reports.push(JSON.parse(argv[argv.length - 1]))
    return { value: { exitCode: opts.reportExit ?? 0, stdout: '', stderr: '' } }
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
