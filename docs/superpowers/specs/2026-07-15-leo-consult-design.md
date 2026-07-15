# leo_consult — one-off subagent consults

**Date:** 2026-07-15
**Status:** Approved

## One sentence

Any supervised Leo agent can dispatch a one-off headless subagent on any
template (any harness/model) for a second opinion, with the answer injected
back into its session as a message.

## Motivation

While working in one agent (e.g. a Claude session), Evan wants a second
opinion from a different model — GPT via codex, qwen via LM Studio, opus —
without leaving the conversation. A "council" (fan the same question out to
several models and reconcile) should be possible, but is deliberately **not**
a built feature: the caller invokes the tool N times in one turn and
reconciles the replies itself, correlated by consult ID. Reconciliation is
what the calling model is good at; hardcoding quorum/synthesis logic in Go
would bake in decisions better varied per question.

## Tool surface

New MCP tool in `internal/mcp/tools.go`:

```
leo_consult(template: string, prompt: string, model?: string)
```

- Shape mirrors Claude Code's Agent tool: `template` plays the role of
  `subagent_type` (carries harness, env, harness_options), `model`
  optionally overrides the template's model, `prompt` must be
  self-contained — the consultant sees none of the caller's conversation.
- Returns **immediately** with a consult ID:
  `dispatched consult c-4f2a to codex (gpt-5.6-sol)`.
- The answer arrives later as an injected turn in the caller's session,
  clearly framed for correlation:

  ```
  [consult c-4f2a · codex/gpt-5.6-sol · 3m12s]
  <answer text>
  ```

## Flow

1. **Dispatch.** MCP server forwards to the daemon over the existing Unix
   socket: `{from: <caller process name>, template, model?, prompt}`.
2. **Validate (synchronous, errors returned as tool errors):**
   - template exists;
   - model override passes the resolved harness's `ValidateModel`;
   - the harness supports one-shot runs (`SupportsKind`);
   - the caller is a supervised agent — it needs a live tmux session to
     receive the reply. Otherwise the tool errors with a clear message.
3. **Run.** Daemon resolves the template's harness, env, and
   `harness_options` exactly as for tasks, builds a one-shot `LaunchSpec`
   with **workspace = the caller's workspace** (from the agent store, not
   the template's workspace), and execs the harness headlessly in a
   goroutine — no tmux, no supervision, no session persistence. Output is
   parsed with the adapter's existing `ParseEvents` into a `Result`.
4. **Reply.** On completion, failure, or timeout (default 10 minutes,
   constant), the daemon injects the framed reply into the caller's session
   via the persistent-task `InjectPrompt` path. Failures inject an error
   notice — a consult is never silently dropped.

## Guardrails

- A Leo-supplied prompt preamble tells the consultant it is advisory:
  analyze and answer; do not modify files. This is **not enforced** — the
  consultant runs with its template's configured permissions, so a template
  with bypass-permissions could edit the caller's workspace. v1 accepts
  that (solo user); enforced read-only would be per-harness surgery.
- A small global cap on concurrent consults (4) so a council fan-out
  doesn't fork-bomb a local model server.

## Not in v1

- Follow-up conversation with a consultant (spawn a real agent for that).
- A council orchestrator (emerges from the primitive; add later as a skill
  if the fan-out pattern keeps being hand-rolled).
- Consult history beyond the daemon log.
- Per-consult timeout knobs or config surface — no new config at all;
  templates already express every consult target, including third-party
  endpoints via their `env:` maps.

## Testing

- Runner-style unit tests through the `execCommand` seam for each harness's
  one-shot argv.
- MCP tool handler tests (arg validation, dispatch response shape).
- Daemon endpoint tests: validation failures, reply injection on success,
  error injection on failure/timeout, concurrency cap.
- `make e2e` before push — this adds a new argv path (PR #97 lesson).
