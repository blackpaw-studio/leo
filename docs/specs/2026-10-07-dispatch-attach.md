# Dispatch attach: a contract for leoterm

Date: 2026-10-07 · Status: implemented

leoterm (a client of the `/api/v1` observe API) shows dispatches and can attach
to an interactive dispatch's TUI. This spec is the contract between Leo and
that client. Every change is additive; `SnapshotVersion` stays 1. A client
checks the `features` list on the SSE `hello` before relying on a field.

| Feature | Meaning |
|---|---|
| `dispatch_attach` | `dispatches[].attachable` and `tmux_target` exist; `leo dispatch attach` works |
| `dispatch_removed` | the `dispatch_removed` SSE event exists |
| `state_seq` | `GET /api/v1/state` carries `meta.seq` |

## 1. Viewer placement `background`

`defaults.dispatch.viewer.placement` (and the per-session
`@leo_viewer_placement` override) accepts `pane | window | background`. The
default stays `pane`.

With `background`, every dispatch viewer opens as a detached window in the
`leo-dispatch` session and never touches the caller's tmux session: interactive
dispatches (nested ones too) and headless viewer windows alike. The recorded
`ViewerKind` is `window`, so the pane reconciler never moves it, and the window
is attachable (section 2).

## 2. `attachable` and `tmux_target`

Each `dispatches[]` entry in `/api/v1/state`, and each `dispatch_changed`
payload, gains:

| Field | Type | Meaning |
|---|---|---|
| `attachable` | bool | `leo dispatch attach <id>` can show this dispatch now |
| `tmux_target` | string, omitted when empty | the interactive dispatch's tmux pane id (`%N`) on Leo's tmux server (`tmux -L leo`) |

`attachable` is true only for an interactive dispatch that has a pane, whose
`ViewerKind` is `window` (the TUI is alone in its own window), and whose status
is not terminal (`done`, `failed`, `timeout`, `canceled`, `closed`, `released`)
or `settling`. A `split` pane (shares the caller's window) and a `hidden` pane
(parked) are never attachable. `tmux_target` is reported whenever the pane
exists, attachable or not. Both fields are derived from the recorded placement,
which the pane reconciler keeps current, so a move republishes
`dispatch_changed`. Go callers use `consult.Attachable(Record)`.

## 3. `leo [--host H] dispatch attach <id>`

1. Fetch the record (`GET /api/dispatch/<id>`) and check `consult.Attachable`.
   An unknown id, a headless run, an ended one (terminal, `closed`,
   `settling` aside), and a split or hidden pane each print one line to stderr
   and exit 1 (`SilenceUsage`/`SilenceErrors`; no tmux is touched).
2. Probe live that the pane's window has exactly one pane
   (`display-message -p -t %N '#{window_id} #{window_panes}'`); the record can
   be stale.
3. `new-session -d -s _watch-<id>-<rand>` (never a `leo-` prefix, which marks
   supervised agents), `link-window` the dispatch's window into it, `kill-window`
   the placeholder window `new-session` made, `set-option status off`.
4. `tmux -L leo attach -r -t =_watch-…` (`-r`: keystrokes never reach the
   dispatch; popup variant inside tmux). Panes are never moved or broken out.
5. Any failure before the attach kills the watch session.

`destroy-unattached` is armed by a `client-attached` hook, not set up front.
Verified against tmux 3.6a on an isolated `TMUX_TMPDIR` socket: setting
`destroy-unattached on` on a session that has no client destroys it at once,
before the attach could start. With the hook, detaching destroys the session
(the dispatch window survives in its own session), and the dispatch's window
closing destroys it and the client exits 0.

Remote: `ssh -tt <host> <leo> dispatch attach '<id>'`, the id a single
shell-quoted token. The remote leo prints its own one-line reason; only its
exit status is propagated.

Known behavior: startup-dialog auto-dismiss skips sessions with an attached
client (`session_attached`). The watch session is a different session, so the
dispatch's own session still reads 0 attached.
