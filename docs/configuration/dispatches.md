# Dispatches and consults

Leo can run a template's harness/model as a one-shot subagent. Both forms are
headless, receive a self-contained prompt, and retain no conversation after
they finish.

## MCP tools

`leo_consult(template, prompt, model?)` is for a quick second opinion. It
waits and returns the consultant's final answer directly. Consults get an
advisory preamble: inspect and answer, but do not modify files.

`leo_dispatch(template, prompt, model?, cwd?, name?)` starts work
asynchronously and immediately returns an ID. Use it for implementation,
review, or exploration that can proceed while the caller does other work.
The prompt must say exactly what the subagent should do; it has no access to
the caller's conversation. `cwd` defaults to the caller's working directory.

`leo_wait(ids, timeout_seconds?)` waits for one or more dispatches and returns
each final result or current status. A timeout leaves still-running IDs
available for another wait. `leo_cancel(id)` cancels a non-terminal dispatch.

All four tools select a named `templates:` entry, not a running agent. The
template supplies the harness, model, environment, and harness options; an
explicit `model` is validated by that harness. `can_consult` permissions apply
to both consult and dispatch targets.

## CLI

```console
leo dispatch run codex-implementer "Add the parser tests" --cwd "$PWD"
leo dispatch list
leo dispatch watch d-12ab34
leo dispatch show d-12ab34
leo dispatch cancel d-12ab34
```

`run` waits and prints the final result. `list` and `watch` also work for
consults, preserving the older `leo consult list|watch` interface. `watch`
accepts an unambiguous ID prefix; Ctrl-C only detaches. `show` prints a record
as JSON. These record-reading commands support `--host`; starting and
cancelling dispatches currently require the local daemon.

## Viewer windows

Every `leo_dispatch` opens a detached window on Leo's dedicated tmux server
(`tmux -L leo`). If its caller is a live supervised agent, the window is added
to that agent's `leo-<caller>` session. Otherwise Leo creates or reuses the
`leo-dispatch` session. The window is named for the dispatch ID and runs
`leo dispatch watch <id>` with `remain-on-exit` enabled, so the completed feed
remains inspectable. Dead viewer windows from prior dispatches are pruned when
the next one opens. Tmux is observability only: any tmux failure is logged and
never prevents a dispatch from running. `leo_consult` does not open a window.

## Records and limits

Records live under `<state>/dispatches/`, with `0600` files in a `0700`
directory:

- `<id>.json` records caller, kind (`dispatch` or `consult`), template,
  harness, model, workspace, prompt, status, timing, and terminal error/text.
- `<id>.ndjson` contains timestamped harness events; malformed output is kept
  as raw text instead of being discarded.

Status moves through `queued → running → done | failed | timeout | canceled`.
At most four runs execute at once; queued work remains cancellable. The run
deadline is 30 minutes. Leo retains the 20 newest settled records and never
prunes plausible in-flight runs.

## Example templates

Use separate templates when the worker and reviewer need different model or
permission profiles:

```yaml
templates:
  codex-implementer:
    harness: codex
    model: gpt-5.6-terra
    workspace: ~/src/project
  codex-reviewer:
    harness: codex
    model: gpt-5.6-sol
    workspace: ~/src/project
```

An orchestrator can dispatch the implementation, then dispatch the reviewer
with the implementation's scope and request the review after it completes.
