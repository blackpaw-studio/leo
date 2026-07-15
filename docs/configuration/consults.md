# Consults — one-off second opinions

Any supervised agent can ask another model for a one-off opinion with the
`leo_consult` MCP tool. Leo runs a headless one-shot subagent on the chosen
template's harness/model in the **caller's workspace** and injects the
answer back into the caller's session as a message.

## Usage (from inside an agent)

- `leo_consult(template: "codex", prompt: "Review this design: …")`
- Optional `model` overrides the template's model (validated against the
  template's harness).
- The tool returns immediately with an id like `c-4f2a`; the reply arrives
  later framed as `[consult c-4f2a · codex/gpt-5.6-sol · 3m12s] …`.
- **Council pattern:** call `leo_consult` several times with different
  templates in one turn, then reconcile the replies as they arrive.

## Semantics and limits

- Templates are the unit of addressing — harness, model, env (including
  third-party endpoints via `env:` maps), and `harness_options` all come
  from the template. There is no consult-specific config.
- The consultant is advisory: a preamble instructs it to analyze without
  modifying files. This is **not enforced** — it runs with the template's
  configured permissions in the caller's workspace.
- One-shot only: no session is kept and no follow-up is possible; spawn a
  real agent for a conversation.
- Timeout 10 minutes; at most 4 consults run concurrently (extra dispatches
  queue). Failures and timeouts are delivered as
  `[consult <id> · … · failed after <elapsed>] <reason>` — never dropped.
- Callers that are config-defined processes without an agent record run the
  consultant in the daemon's working directory (no workspace to inherit).
