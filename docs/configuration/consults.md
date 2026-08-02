# Consults — one-off second opinions

Any Leo process can ask another model for a one-off opinion with the
`leo_consult` MCP tool. Leo runs the selected template's harness/model as a
headless one-shot process in the caller's workspace and returns its final
answer directly as the tool result.

## Usage

- `leo_consult(template: "codex", prompt: "Review this design: …")`
- `template` names a template, not a running agent — "consult fable" means
  the `fable` template, not an agent called fable. Asking an agent to
  consult a model is `leo_consult`; handing work to a running agent is
  `leo_send_message`. An unknown template comes back with the valid names.
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

## Watching a consult

Consults are started by agents, not by you, and a long one is otherwise a
silent ten-minute gap. Every consult records what it does, so you can watch
it work:

```console
$ leo consult list
ID          CALLER   TEMPLATE  MODEL          ELAPSED  STATUS
c-7f3a2b1e  leo      codex     gpt-5.3-codex  1:42     running
c-3c9d10a4  olympus  local     qwen3.6-35b    4:11     done

$ leo consult watch
[consult c-7f3a2b1e · codex/gpt-5.3-codex · from leo · running]
Review this design: …

   0:01  read     internal/consult/consult.go
   0:04  grep     CombinedOutput
   0:09  text     The dispatcher discards everything but the final text.
   0:11  bash     go test ./internal/consult/
[done after 1:58]
```

- `leo consult watch` with no argument picks the newest running consult,
  falling back to the most recent one. An id may be abbreviated to any
  unique prefix.
- Ctrl-C detaches. The consult keeps running; there is no way to cancel one
  from the CLI.
- Consults that are queued behind the concurrency limit appear in `list`
  before they start, so a stalled call is distinguishable from a busy one.
- Both commands accept `--host` and read the records on that host.

The feed shows the consultant's own text, the tools it invokes, and its
final answer. Successful tool *results* are omitted — the call already says
what it did, and the bodies would bury everything else — but failures are
shown. Add `--json` to `list` for the raw records.

## Where recordings live

Under `<state>/consults/`, two files per consult, both `0600` in a `0700`
directory:

- `<id>.json` — caller, template, harness, model, workspace, prompt,
  status, and timing. Status runs `queued → running → done | failed |
  timeout | canceled`.
- `<id>.ndjson` — one line per harness event, `{"t": <seconds since start>,
  "d": <the harness event verbatim>}`. Output that was not JSON — a
  crashing harness, stderr chatter — is captured as `{"t": …, "raw": "…"}`
  instead of being lost. `jq .d` recovers the untouched harness stream.

The 20 most recent consults are kept; consults still in flight are never
pruned. Recordings contain whatever the consultant read, the same trust
boundary as task logs under `<state>/logs`.
