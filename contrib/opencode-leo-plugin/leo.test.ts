/**
 * Run with: bun test contrib/opencode-leo-plugin/leo.test.ts
 *
 * Covers the paths that are awkward to reach through a live agent turn: bad
 * config, a rejecting daemon, and the wire format the receiving Leo agent
 * actually sees.
 */
import { expect, test, beforeAll, afterAll } from "bun:test"
import { LeoMessaging } from "./leo"

type Received = { path: string; auth: string | null; body: any }
const received: Received[] = []
let ok: any
let rejecting: any

const ctx = (sessionID = "ses_test123") =>
  ({ sessionID, messageID: "msg_1", agent: "build", abort: new AbortController().signal }) as any

async function toolWith(env: Record<string, string>) {
  const saved = { ...process.env }
  Object.assign(process.env, env)
  try {
    const hooks = await LeoMessaging({} as any)
    return hooks.tool!.message_leo
  } finally {
    for (const k of Object.keys(env)) delete (process.env as any)[k]
    Object.assign(process.env, saved)
  }
}

beforeAll(() => {
  ok = Bun.serve({
    port: 0,
    async fetch(req) {
      received.push({
        path: new URL(req.url).pathname,
        auth: req.headers.get("Authorization"),
        body: await req.json(),
      })
      return Response.json({ ok: true })
    },
  })
  rejecting = Bun.serve({
    port: 0,
    fetch: () =>
      new Response('{"error":"no such agent \\"rocket\\"; running: olympus, jukebox, blog"}', {
        status: 404,
      }),
  })
})

afterAll(() => {
  ok?.stop(true)
  rejecting?.stop(true)
})

const baseEnv = (url: string) => ({
  LEO_URL: url,
  LEO_TOKEN: "tok-abc",
  LEO_TARGET: "rocket",
  LEO_CLIENT_NAME: "docker-scout",
})

test("delivers Leo's wire format carrying the originating session as the reply address", async () => {
  received.length = 0
  const t = await toolWith(baseEnv(ok.url.origin))
  const result: any = await t.execute({ text: "build finished" }, ctx("ses_abc999"))

  expect(received).toHaveLength(1)
  expect(received[0].path).toBe("/web/agent/rocket/message")
  expect(received[0].auth).toBe("Bearer tok-abc")
  expect(received[0].body.text).toBe("[message from docker-scout#ses_abc999] build finished")
  expect(received[0].body.from).toBe("docker-scout#ses_abc999")
  expect(result.output).toContain("ses_abc999")
})

test("percent-encodes the target so it cannot escape the message route", async () => {
  received.length = 0
  const t = await toolWith({ ...baseEnv(ok.url.origin), LEO_TARGET: "../../api/agent/spawn" })
  await t.execute({ text: "x" }, ctx())
  expect(received[0].path).toBe("/web/agent/..%2F..%2Fapi%2Fagent%2Fspawn/message")
})

test("names the missing variable instead of vanishing from the tool list", async () => {
  const t = await toolWith({ LEO_URL: ok.url.origin, LEO_TOKEN: "tok" })
  expect(t.execute({ text: "x" }, ctx())).rejects.toThrow(/LEO_TARGET, LEO_CLIENT_NAME/)
})

test("rejects a non-http LEO_URL", async () => {
  const t = await toolWith({ ...baseEnv("file:///etc/passwd") })
  expect(t.execute({ text: "x" }, ctx())).rejects.toThrow(/must be http or https/)
})

test("never leaks the daemon's error body — which names other agents", async () => {
  const t = await toolWith(baseEnv(rejecting.url.origin))
  try {
    await t.execute({ text: "x" }, ctx())
    throw new Error("expected a rejection")
  } catch (err) {
    const msg = String(err)
    expect(msg).toContain("HTTP 404")
    expect(msg).not.toContain("olympus")
    expect(msg).not.toContain("jukebox")
  }
})

test("never leaks credentials embedded in LEO_URL", async () => {
  // Port 1 refuses instantly, so this exercises the unreachable-host error text.
  const t = await toolWith(baseEnv("http://user:hunter2@127.0.0.1:1"))
  try {
    await t.execute({ text: "x" }, ctx())
    throw new Error("expected a rejection")
  } catch (err) {
    const msg = String(err)
    expect(msg).not.toContain("hunter2")
    expect(msg).not.toContain("user:")
  }
})

test("reports cancellation as cancellation, not as an unreachable daemon", async () => {
  const controller = new AbortController()
  const t = await toolWith(baseEnv("http://127.0.0.1:1"))
  controller.abort()
  const context = { ...ctx(), abort: controller.signal } as any
  expect(t.execute({ text: "x" }, context)).rejects.toThrow(/cancelled/)
})
