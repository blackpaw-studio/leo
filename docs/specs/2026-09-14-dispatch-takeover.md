# Dispatch takeover: continue a finished subagent interactively

Extends `docs/specs/2026-09-13-subagent-dispatch.md`.

## Goal

A finished dispatch's viewer window can become an interactive session on the
same harness with the subagent's full context, so a user can read the result
and redirect the same run from its pane. The headless run and its structured
result are unchanged.

## Non-goals

- Taking over a run that is still executing. That needs a verified clean
  cancel-then-resume of a partial rollout; not in this spec.
- Returning anything from the interactive session to the orchestrator. Once a
  human takes over, the dispatch is theirs; `leo_wait` already returned.
- Harnesses without a resumable session id. Codex and Claude both report one
  in their event streams; OpenCode is out of scope until it does.

## Design

### Record

`Record` gains `session_id` (string, JSON `session_id,omitempty`), taken from
`ParseEvents(...).SessionID` when the run completes. It is persisted through
the recorder handle like `viewer_window_id`, never via a raw write.

### Resume command from the daemon

`GET /api/dispatch/{id}/resume` returns `{argv: [...], env: {...}, cwd}` for a
terminal record that has a `session_id`, or 404 with a reason otherwise
(`still running`, `no session id`, `harness cannot resume`). The daemon builds
it from the record's template exactly as an interactive agent launch would:

```
spec := harness.LaunchSpec{
    Kind: harness.KindAgent, Name: rec.ID, Model: rec.Model,
    Workspace: rec.Cwd, Options: decoded template harness_options,
    Session: harness.Session{Mode: harness.SessionResume, ID: rec.SessionID},
}
argv = [h.Binary()] + h.Args(spec); env = merged(harnessEnv, template env)
```

So the interactive session keeps the template's permission mode and env. The
harness-side details stay in the adapters; the CLI never assembles harness
argv itself.

### Watch handoff

`leo dispatch watch <id>` today prints `[done after …]` and exits. After a
terminal status it instead:

1. Prints one line: `press Enter to continue this session interactively, any
   other key to close`.
2. Waits for a single key on the terminal. If stdin is not a terminal, exits
   as today (scripts and pipes are unaffected).
3. On Enter, calls the resume endpoint and replaces itself with the returned
   command (`syscall.Exec`) in the returned cwd with the returned env merged
   over the current environment. The pane's process is now the harness TUI;
   its startup dialogs are the user's to answer.
4. On any other key, or a resume error (printed), exits as today.

The same handoff applies to failed, canceled, and timed-out runs when a session
id exists, since redirecting a failed run is the main use.

### Close-on-collection

Collecting a `done` result closes the viewer window today. That must not kill
a session someone has taken over. Before `kill-window`, the viewer checks the
pane: `display-message -p -t <window_id> '#{pane_current_command}'`. If it is
the harness binary (from `h.Binary()`), the window is left alone and the
record is marked `taken_over: true`. If it is `leo` or the pane is dead, the
window closes as before. The one-hour sweep applies the same check.

`leo dispatch list/show` display `taken_over`. `leo_wait` is unaffected: it
already returned before any takeover can happen.

### CLI

`leo dispatch resume <id>` runs the same handoff from any terminal, for a
window that was already closed. It uses the resume endpoint, so the argv and
env come from the daemon.

## Error handling

- Resume endpoint on a running or unknown id: 404 with reason; watch prints it
  and exits.
- Harness that does not support `SessionResume` for `KindAgent`: 404 `harness
  cannot resume`; watch prints it and exits.
- `syscall.Exec` failure: printed, exit 1; the record is not marked taken over
  because the pane still runs `leo`.
- A takeover whose TUI exits leaves a dead pane; the sweep closes it after the
  grace period like any other dead viewer.

## Testing

- Record persistence: a completed run's on-disk record carries `session_id`
  for both codex (`thread_id`) and claude (`session_id`) event shapes.
- Resume endpoint: argv/env/cwd for a codex record match the codex adapter's
  interactive resume argv (asserted against `h.Args`); 404 cases for running,
  no session id, unknown id.
- Watch: with a fake terminal, Enter triggers the exec seam with the
  endpoint's argv; other key exits 0; non-terminal stdin exits without
  prompting.
- Viewer: collection with `pane_current_command` = `codex` leaves the window
  and marks `taken_over`; with `leo` it kills the window; the sweep honours the
  same rule. Argv asserted through the stubbed seam.
- e2e: dispatch a trivial codex run, send Enter to the viewer pane, assert the
  pane's current command becomes `codex` and the window survives collection.
- Live before merge: take over a real finished dispatch from this session,
  send a follow-up prompt in the TUI, confirm context carried.
