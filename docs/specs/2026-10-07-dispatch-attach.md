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
