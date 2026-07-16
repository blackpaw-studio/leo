# Consults — one-off second opinions

Any Leo process can ask another model for a one-off opinion with the
`leo_consult` MCP tool. Leo runs the selected template's harness/model as a
headless one-shot process in the caller's workspace and returns its final
answer directly as the tool result.

## Usage

- `leo_consult(template: "codex", prompt: "Review this design: …")`
- Optional `model` overrides the template's model and is validated by that
  template's harness.
- The call waits for the consultant to finish, like a normal subagent tool.
- For a council, issue concurrent calls to several templates and reconcile
  the returned answers in the same turn.

## Semantics and limits

- Templates supply the harness, model, environment, and `harness_options`.
- Claude runs headlessly through its print/task mode, Codex through
  `codex exec --json`, and OpenCode through `opencode run --format json`.
- The consultant is advisory: a preamble instructs it to analyze without
  modifying files. This is not enforced; configured template permissions
  still apply.
- One-shot only: no session is retained and no follow-up conversation exists.
- Calls time out after 10 minutes. At most four run concurrently; additional
  calls wait and remain cancellable by their callers.
- Supervised agents contribute their workspace. Other Leo callers are also
  supported and run from the daemon's working directory when no workspace can
  be resolved.
